"""AIHUB compositing and independent-photo NR with bounded worker buffers."""
import importlib
import numpy as np


def composite(original, enhanced, *, detail, color):
    if original.shape != enhanced.shape or original.dtype != np.uint8 or enhanced.dtype != np.uint8:
        raise ValueError('NR composite requires matching RGB uint8 frames')
    if not 0 <= detail <= 2 or not 0 <= color <= 1:
        raise ValueError('Invalid NR composite controls')
    if detail == 0:
        return original.copy()
    if detail == 1 and color == 1:
        return np.ascontiguousarray(enhanced)
    def linear(frame):
        s = frame.astype(np.float32) / 255
        return np.where(s <= .04045, s / 12.92, ((s + .055) / 1.055) ** 2.4)
    base, result = linear(original), linear(enhanced)
    weights = np.array([.2126, .7152, .0722], np.float32)
    luminance_ratio = (result @ weights) / np.maximum(base @ weights, 1e-4)
    preserved = base * luminance_ratio[..., None]
    mixed = np.clip(base + (preserved + (result - preserved) * color - base) * detail, 0, 1)
    srgb = np.where(mixed <= .0031308, mixed * 12.92, 1.055 * mixed ** (1 / 2.4) - .055)
    return np.ascontiguousarray(np.clip(np.rint(srgb * 255), 0, 255).astype(np.uint8))


class GatewayAIHubEnhanceImages:
    @classmethod
    def INPUT_TYPES(cls):
        return {'required': {'images': ('IMAGE',), 'settings': ('DLSS5_SETTINGS',)}}

    RETURN_TYPES = ('IMAGE',)
    FUNCTION = 'run'
    CATEGORY = 'gateway/image'

    def run(self, images, settings):
        import nodes
        import torch
        import comfy.model_management as memory
        from comfy.utils import ProgressBar
        root = nodes.NODE_CLASS_MAPPINGS['DLSS5EnhanceImages'].__module__.split('.nodes.')[0]
        common = importlib.import_module(root + '.nodes.common')
        session_cls = importlib.import_module(root + '.dlss5.session').DlssSession
        count, height, width, channels = images.shape
        if channels != 3 or count < 1 or count * width * height > 512_000_000:
            raise ValueError('Invalid AIHUB photo batch')
        memory.throw_exception_if_processing_interrupted()
        images = images.detach().cpu()
        memory.unload_all_models(); memory.soft_empty_cache(force=True)
        result = torch.empty_like(images, device='cpu')
        progress = ProgressBar(count)
        with session_cls(common.resolve_layout(settings), settings.options,
                         input_width=width, input_height=height, frame_count=count) as session:
            if (session.output_width, session.output_height) != (width, height):
                raise ValueError('AIHUB photo NR requires native-size settings')
            for index in range(count):
                memory.throw_exception_if_processing_interrupted()
                rgb = images[index].clamp(0,1).mul(255).round().to(torch.uint8).numpy()
                rgba = np.concatenate((rgb, np.full((height,width,1),255,np.uint8)),axis=2)
                motion = np.zeros((height,width,2), dtype=np.float16)
                enhanced, _ = session.submit(index=index, rgba=rgba, motion=motion, reset=True, pts=index)
                result[index] = torch.from_numpy(composite(rgb,enhanced[...,:3],detail=.6,color=1)).float().div_(255)
                progress.update(1)
        common.confirm_feature_18(session, True)
        return (result,)
