"""Bounded-memory CFR media transport. No ComfyUI or GPU dependency."""
from collections import deque
from fractions import Fraction
import json
import shutil
import subprocess
import threading

import numpy as np
from .lifecycle import WorkerGuard


class Process:
    def __init__(self, args, *, read=False, cancelled=lambda: False):
        self.logs = deque(maxlen=16)
        self.proc = subprocess.Popen(args, stdin=subprocess.DEVNULL if read else subprocess.PIPE,
                                     stdout=subprocess.PIPE if read else subprocess.DEVNULL,
                                     stderr=subprocess.PIPE,
                                     creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        self.guard = WorkerGuard(self.proc, cancelled=cancelled)
        self.thread = threading.Thread(target=self._drain, daemon=True)
        self.thread.start()

    def _drain(self):
        for line in iter(self.proc.stderr.readline, b''):
            self.logs.append(line[:2048].decode('utf-8', 'replace'))

    def read(self, size, timeout=30):
        with self.guard.operation(timeout):
            data = bytearray()
            while len(data) < size:
                part = self.proc.stdout.read(size-len(data))
                if not part:
                    break
                data.extend(part)
            return data

    def write(self, data):
        with self.guard.operation(30):
            self.proc.stdin.write(data)
            self.proc.stdin.flush()

    def finish(self):
        with self.guard.operation(120):
            if self.proc.stdin:
                self.proc.stdin.close()
            rc = self.proc.wait(timeout=120)
        if rc:
            raise RuntimeError('Media process failed: '+''.join(self.logs)[-2000:])

    def close(self):
        self.guard.close()
        for stream in (self.proc.stdin, self.proc.stdout):
            if stream and not stream.closed:
                stream.close()
        self.thread.join(timeout=2)
        if self.proc.stderr:
            self.proc.stderr.close()


def probe(ffprobe, path, cancelled=lambda: False):
    process = Process([ffprobe, '-v', 'error', '-count_frames', '-show_entries',
                       'stream=codec_type,width,height,nb_read_frames,avg_frame_rate,duration:format=duration',
                       '-of', 'json', str(path)], read=True, cancelled=cancelled)
    try:
        raw = process.read(65537, timeout=120)
        if len(raw) > 65536:
            raise ValueError('Unexpected media metadata size')
        process.finish()
        return json.loads(raw)
    finally:
        process.close()


def frames(ffmpeg, path, width, height, count, cancelled=lambda: False):
    process = Process([ffmpeg, '-nostdin', '-v', 'error', '-xerror', '-i', str(path),
                       '-map', '0:v:0', '-an', '-sn', '-dn', '-fps_mode', 'passthrough',
                       '-pix_fmt', 'rgb24', '-f', 'rawvideo', 'pipe:1'], read=True, cancelled=cancelled)
    size = width*height*3
    try:
        for _ in range(count):
            raw = process.read(size)
            if len(raw) != size:
                raise ValueError('Video ended before its declared frame count')
            yield np.frombuffer(raw, dtype=np.uint8).reshape(height, width, 3)
        if process.read(1):
            raise ValueError('Video contains unexpected additional frames')
        process.finish()
    finally:
        process.close()


class Encoder:
    def __init__(self, ffmpeg, path, width, height, fps, *, crf=None, audio=None,
                 count=None, cancelled=lambda: False):
        self.shape = (height, width, 3)
        self.count = 0
        self.expected = count
        rate = Fraction(fps)
        args = [ffmpeg, '-nostdin', '-v', 'error', '-y', '-f', 'rawvideo', '-pix_fmt',
                'rgb24', '-s', f'{width}x{height}', '-r', f'{rate.numerator}/{rate.denominator}', '-i', 'pipe:0']
        if audio:
            if count is None:
                raise ValueError('Audio requires an exact video duration')
            args += ['-i', str(audio), '-map', '0:v:0', '-map', '1:a:0', '-af', 'apad',
                     '-t', str(float(Fraction(count, 1)/rate)), '-c:a', 'aac', '-b:a', '192k']
        else:
            args += ['-map', '0:v:0', '-an']
        if crf is None:
            args += ['-c:v', 'ffv1', '-level', '3', '-pix_fmt', 'bgr0']
        else:
            args += ['-c:v', 'libx264', '-preset', 'medium', '-crf', str(crf),
                     '-pix_fmt', 'yuv420p', '-movflags', '+faststart']
        args += ['-threads', '2', str(path)]
        self.process = Process(args, cancelled=cancelled)

    def write(self, frame):
        if frame.shape != self.shape or frame.dtype != np.uint8:
            raise ValueError('Unexpected encoder frame shape or dtype')
        self.process.write(np.ascontiguousarray(frame).tobytes())
        self.count += 1

    def finish(self):
        if self.expected is not None and self.count != self.expected:
            raise ValueError(f'Incorrect frame count: {self.count} != {self.expected}')
        self.process.finish()

    def close(self):
        self.process.close()


def reserve_disk(directory, width, height, count, multiplier):
    # Raw source + cascades + lossless intermediates + publication headroom.
    required = width*height*count*4*(2*multiplier+4)+(2 << 30)
    if shutil.disk_usage(directory).free < required:
        raise ValueError(f'Insufficient temporary disk space: need {required // (1 << 20)} MiB')
    return required


def verify_video(ffprobe, path, width, height, count, fps, cancelled=lambda: False, *, require_audio=False):
    info = probe(ffprobe, path, cancelled)
    video = next(s for s in info['streams'] if s['codec_type'] == 'video')
    if (video['width'], video['height'], int(video['nb_read_frames'])) != (width, height, count):
        raise ValueError('Encoded video dimensions or frame count differ from requested output')
    if Fraction(video['avg_frame_rate']) != Fraction(fps):
        raise ValueError('Encoded frame rate differs from requested output')
    if require_audio:
        audio = next((s for s in info['streams'] if s['codec_type'] == 'audio'), None)
        duration = float(Fraction(count, 1)/Fraction(fps))
        if audio is None or abs(float(audio.get('duration', 0))-duration) > 0.05:
            raise ValueError('Encoded audio is missing or differs from the video duration')
    return info
