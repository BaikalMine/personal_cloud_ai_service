import json
from pathlib import Path
import shutil
import sys
import tempfile
import threading
import time
import unittest
import wave
import numpy as np
from .core import Encoder, Process, frames, verify_video


class TransportTests(unittest.TestCase):
    def test_roundtrip_and_short_audio(self):
        ffmpeg, ffprobe = shutil.which('ffmpeg'), shutil.which('ffprobe')
        self.assertTrue(ffmpeg and ffprobe)
        with tempfile.TemporaryDirectory() as directory:
            directory=Path(directory)
            for samples in (2400, 96000):
                audio=directory/'sound.wav'
                with wave.open(str(audio),'wb') as f:
                    f.setnchannels(1); f.setsampwidth(2); f.setframerate(48000)
                    f.writeframes(bytes(samples*2))
                target=directory/'out.mp4'
                encoder=Encoder(ffmpeg,target,64,48,48,crf=18,audio=audio,count=48)
                try:
                    for index in range(48):
                        encoder.write(np.full((48,64,3),index*4,np.uint8))
                    encoder.finish()
                finally: encoder.close()
                info=verify_video(ffprobe,target,64,48,48,48,require_audio=True)
                sound=next(s for s in info['streams'] if s['codec_type']=='audio')
                self.assertAlmostEqual(float(sound['duration']),1.0,delta=.05)
                self.assertEqual(sum(1 for _ in frames(ffmpeg,target,64,48,48)),48)

    def test_cancel_blocked_read(self):
        cancel=threading.Event()
        process=Process([sys.executable,'-c','import time; time.sleep(60)'],read=True,cancelled=cancel.is_set)
        timer=threading.Timer(.2,cancel.set); timer.start()
        start=time.monotonic()
        try:
            with self.assertRaises(InterruptedError): process.read(1)
        finally:
            process.close(); timer.cancel()
        self.assertLess(time.monotonic()-start,5)
        self.assertIsNotNone(process.proc.poll())

    def test_bad_frame_count(self):
        with tempfile.TemporaryDirectory() as directory:
            encoder=Encoder(shutil.which('ffmpeg'),Path(directory)/'out.mkv',64,48,24,count=2)
            try:
                encoder.write(np.zeros((48,64,3),np.uint8))
                with self.assertRaises(ValueError): encoder.finish()
            finally: encoder.close()

    def test_missing_audio_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            target=Path(directory)/'out.mp4'
            encoder=Encoder(shutil.which('ffmpeg'),target,64,48,24,count=2,crf=18)
            try:
                for _ in range(2): encoder.write(np.zeros((48,64,3),np.uint8))
                encoder.finish()
            finally: encoder.close()
            with self.assertRaisesRegex(ValueError,'audio'):
                verify_video(shutil.which('ffprobe'),target,64,48,2,24,require_audio=True)

    def test_gateway_schema_matches_node(self):
        from . import GatewayVideoStream
        fixture=Path(__file__).resolve().parents[2]/'internal/gateway/testdata/gateway_video_stream_object_info.json'
        expected=json.loads(fixture.read_text())['GatewayVideoStream']['input']
        self.assertEqual(json.loads(json.dumps(GatewayVideoStream.INPUT_TYPES())),expected)


if __name__=='__main__': unittest.main()
