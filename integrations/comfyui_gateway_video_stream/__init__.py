"""Gateway video output with bounded postprocessing buffers."""
from contextlib import closing
from fractions import Fraction
import importlib
import os
from pathlib import Path
import shutil
import tempfile
import uuid
import wave

import numpy as np
from .core import Encoder, frames, reserve_disk, verify_video


class GatewayVideoStream:
    RTX_FIRST = False
    NR_COLOR = 1.0
    @classmethod
    def INPUT_TYPES(cls):
        return {'required': {
            'images': ('IMAGE',), 'audio': ('AUDIO',),
            'fg_multiplier': ('INT', {'default': 1, 'min': 1, 'max': 4}),
            'nr_enabled': ('BOOLEAN', {'default': False}),
            'rtx_scale': ('FLOAT', {'default': 1.0, 'min': 1.0, 'max': 4.0}),
            'rtx_enabled': ('BOOLEAN', {'default': False}),
            'rtx_quality': (['LOW', 'MEDIUM', 'HIGH', 'ULTRA'],),
            'crf': ('INT', {'default': 18, 'min': 0, 'max': 51}),
            'filename_prefix': ('STRING', {'default': 'AIHub/Video'}),
        }, 'optional': {'settings': ('DLSS5_SETTINGS',)}}

    RETURN_TYPES = ()
    FUNCTION = 'run'
    OUTPUT_NODE = True
    CATEGORY = 'gateway/video'

    def run(self, images, audio, fg_multiplier, nr_enabled, rtx_scale, rtx_enabled,
            rtx_quality, crf, filename_prefix, settings=None):
        import folder_paths
        import nodes
        import torch
        import comfy.model_management as memory
        from comfy.utils import ProgressBar

        count, height, width, channels = images.shape
        if count < 2 or count > 730 or channels != 3 or min(height, width) < 64 or count*height*width > 1_000_000_000:
            raise ValueError('Invalid source video shape')
        if fg_multiplier not in (1, 2, 3, 4):
            raise ValueError('Invalid interpolation multiplier')
        if nr_enabled and settings is None:
            raise ValueError('DLSS settings are required')
        out_w = max(8, round(width*rtx_scale/8)*8) if rtx_enabled else width
        out_h = max(8, round(height*rtx_scale/8)*8) if rtx_enabled else height
        if max(out_w, out_h) > 7680 or min(out_w, out_h) > 4320 or out_w*out_h > 16_777_216:
            raise ValueError('Output frame exceeds the native processing bounds')
        ffmpeg = shutil.which('ffmpeg')
        ffprobe = shutil.which('ffprobe')
        if not ffmpeg or not ffprobe:
            for directory in folder_paths.get_folder_paths('custom_nodes'):
                binary = Path(directory)/'ComfyUI-DLSS5-Enhancer'/'ffmpeg'/'bin'
                if (binary/'ffmpeg.exe').is_file() and (binary/'ffprobe.exe').is_file():
                    ffmpeg, ffprobe = str(binary/'ffmpeg.exe'), str(binary/'ffprobe.exe')
                    break
        if not ffmpeg or not ffprobe:
            raise RuntimeError('ffmpeg and ffprobe must be available to the video executor')
        target_dir, filename, counter, subfolder, _ = folder_paths.get_save_image_path(
            filename_prefix, folder_paths.get_output_directory(), out_w, out_h)
        root = Path(folder_paths.get_output_directory()).resolve()
        target_dir = Path(target_dir).resolve()
        if not target_dir.is_relative_to(root):
            raise ValueError('Output must stay within ComfyUI output')
        target_dir.mkdir(parents=True, exist_ok=True)
        temporary = Path(folder_paths.get_temp_directory())
        temporary.mkdir(parents=True, exist_ok=True)
        reserve_disk(temporary, max(width, out_w), max(height, out_h), count, fg_multiplier)
        final = target_dir / f'{filename}_{uuid.uuid4().hex}.mp4'
        partial = final.with_suffix('.partial.mp4')
        cancelled = memory.processing_interrupted
        check = memory.throw_exception_if_processing_interrupted
        total = count*fg_multiplier
        fps = 24*fg_multiplier
        progress = ProgressBar(count + total + (total if fg_multiplier > 1 else 0)
                               + (total if nr_enabled else 0)
                               + ((count if self.RTX_FIRST else total) if rtx_enabled else 0))
        step = 0

        def tick():
            nonlocal step
            check(); step += 1; progress.update_absolute(step)

        def encode(path, iterable, w, h, n, rate, quality=None, sound=None):
            writer = Encoder(ffmpeg, path, w, h, rate, crf=quality, audio=sound,
                             count=n, cancelled=cancelled)
            try:
                with closing(iterable):
                    for frame in iterable:
                        check(); writer.write(frame); tick()
                writer.finish()
            finally:
                writer.close()

        def upscale(source, target, w, h, n, rate):
            import nvvfx
            quality = getattr(nvvfx.effects.QualityLevel, rtx_quality)
            with nvvfx.VideoSuperRes(quality) as sr:
                sr.output_width = out_w; sr.output_height = out_h; sr.load()
                def upscaled():
                    with closing(frames(ffmpeg, source, w, h, n, cancelled)) as stream:
                        for rgb in stream:
                            check()
                            tensor = torch.from_numpy(rgb.copy()).cuda().permute(2,0,1).float().div_(255).contiguous()
                            out = torch.from_dlpack(sr.run(tensor).image).movedim(0,-1)
                            yield out.clamp(0,1).mul(255).round().to(torch.uint8).cpu().numpy()
                encode(target, upscaled(), out_w, out_h, n, rate)

        try:
            with tempfile.TemporaryDirectory(prefix='gateway-video-', dir=temporary) as work:
                work = Path(work)
                source = work/'source.mkv'
                def original():
                    for index in range(count):
                        check()
                        yield images[index].detach().to('cpu').clamp(0, 1).mul(255).round().to(torch.uint8).numpy()
                encode(source, original(), width, height, count, 24)
                memory.unload_all_models(); memory.soft_empty_cache(force=True)
                current = source
                if rtx_enabled and self.RTX_FIRST:
                    current = work/'rtx.mkv'
                    upscale(source, current, width, height, count, 24)
                    width, height = out_w, out_h
                if fg_multiplier > 1:
                    fg_root = nodes.NODE_CLASS_MAPPINGS['RH_DLSS5FrameInterpolation'].__module__.rsplit('.nodes', 1)[0]
                    fg = importlib.import_module(fg_root+'.dlss5nr.fg')
                    decoded = frames(ffmpeg, current, width, height, count, cancelled)
                    next_index = 0
                    def access(index):
                        nonlocal next_index
                        if index != next_index:
                            raise ValueError('Non-sequential frame access')
                        rgb = next(decoded); next_index += 1
                        rgba = np.empty((height, width, 4), np.uint8)
                        rgba[..., :3] = rgb; rgba[..., 3] = 255
                        return rgba, None
                    current = work/'fg.mkv'
                    writer = Encoder(ffmpeg, current, width, height, fps, count=total, cancelled=cancelled)
                    last = None
                    def sink(rgb):
                        nonlocal last
                        check(); writer.write(rgb); last = rgb.copy(); tick()
                    try:
                        with closing(decoded):
                            fg.interpolate_stream_from_source(access, count, width, height, Fraction(24), fg_multiplier,
                                                              scene_threshold=.15, frame_sink=sink)
                        if writer.count != (count-1)*fg_multiplier+1 or last is None:
                            raise ValueError('Unexpected DLSS FG output frame count')
                        for _ in range(fg_multiplier-1):
                            sink(last)
                        writer.finish()
                    finally:
                        writer.close()
                if nr_enabled:
                    nr_root = nodes.NODE_CLASS_MAPPINGS['DLSS5EnhanceImages'].__module__.split('.nodes.')[0]
                    common = importlib.import_module(nr_root+'.nodes.common')
                    motion = importlib.import_module(nr_root+'.dlss5.motion')
                    imaging = importlib.import_module(nr_root+'.dlss5.imaging')
                    session_cls = importlib.import_module(nr_root+'.dlss5.session').DlssSession
                    nr_source = current; current = work/'nr.mkv'
                    with session_cls(common.resolve_layout(settings), settings.options,
                                     input_width=width, input_height=height, frame_count=total) as session:
                        if (session.output_width, session.output_height) != (width, height):
                            raise ValueError('Streaming NR currently requires native-size settings')
                        guide = motion.TemporalGuide(session.render_width, session.render_height,
                            flow_width=settings.options.flow_width, scene_change_threshold=settings.options.scene_change_threshold,
                            enabled=settings.options.wants_motion(total))
                        def enhanced():
                            with closing(frames(ffmpeg, nr_source, width, height, total, cancelled)) as stream:
                                for index, rgb in enumerate(stream):
                                    check()
                                    rgba = np.concatenate((rgb, np.full((*rgb.shape[:2],1),255,np.uint8)), axis=2)
                                    rgba = imaging.fit_frame(rgba, session.render_width, session.render_height)
                                    temporal = guide.process(rgba)
                                    out, _ = session.submit(index=index, rgba=rgba, motion=temporal.motion, reset=temporal.reset, pts=index)
                                    from .nr import composite
                                    yield composite(rgb, out[..., :3], detail=1.0, color=self.NR_COLOR)
                        encode(current, enhanced(), width, height, total, fps)
                    common.confirm_feature_18(session, True)
                if rtx_enabled and not self.RTX_FIRST:
                    rtx_source = current; current = work/'rtx.mkv'
                    upscale(rtx_source, current, width, height, total, fps)
                wav = work/'audio.wav'
                waveform = audio['waveform'].detach().cpu()
                if waveform.ndim != 3 or waveform.shape[0] != 1 or waveform.shape[1] not in (1,2):
                    raise ValueError('Unsupported audio shape')
                with wave.open(str(wav), 'wb') as output:
                    output.setnchannels(waveform.shape[1]); output.setsampwidth(2); output.setframerate(int(audio['sample_rate']))
                    output.writeframes(waveform[0].clamp(-1,1).mul(32767).round().to(torch.int16).t().contiguous().numpy().tobytes())
                encode(partial, frames(ffmpeg, current, out_w, out_h, total, cancelled), out_w, out_h, total, fps, crf, wav)
                verify_video(ffprobe, partial, out_w, out_h, total, fps, cancelled, require_audio=True)
                check(); os.replace(partial, final)
            return {'ui': {'videos': [{'filename': final.name, 'subfolder': subfolder, 'type':'output', 'format':'video/mp4'}]}}
        finally:
            partial.unlink(missing_ok=True)


class GatewayAIHubVideoStream(GatewayVideoStream):
    RTX_FIRST = True
    NR_COLOR = 0.0


from .nr import GatewayAIHubEnhanceImages
from .reference import GatewayAIHubReferenceFrames

NODE_CLASS_MAPPINGS = {'GatewayVideoStream': GatewayVideoStream,
                       'GatewayAIHubVideoStream': GatewayAIHubVideoStream,
                       'GatewayAIHubEnhanceImages': GatewayAIHubEnhanceImages,
                       'GatewayAIHubReferenceFrames': GatewayAIHubReferenceFrames}
NODE_DISPLAY_NAME_MAPPINGS = {'GatewayVideoStream': 'Gateway Streaming Video',
                              'GatewayAIHubVideoStream': 'Gateway AIHUB Streaming Video',
                              'GatewayAIHubEnhanceImages': 'Gateway AIHUB Photo Enhancement'}
