from types import SimpleNamespace as NS
from unittest.mock import patch
import sys
import unittest
import torch
from .reference import GatewayAIHubReferenceFrames


class ReferenceTests(unittest.TestCase):
    def test_padding_preserves_every_source_frame(self):
        memory=NS(throw_exception_if_processing_interrupted=lambda:None)
        with patch.dict(sys.modules,{'comfy':NS(model_management=memory),'comfy.model_management':memory}):
            for count,expected in ((5,5),(50,56),(120,124),(124,124),(240,243),(720,736)):
                source=torch.arange(count).reshape(count,1,1,1).expand(count,2,2,3).float()
                output,aligned,original=GatewayAIHubReferenceFrames().run(source)
                self.assertEqual((aligned,original),(expected,count))
                torch.testing.assert_close(source,output[:count])
                if expected>count:torch.testing.assert_close(output[count:],source[-1:].expand(expected-count,2,2,3))
                else:self.assertIs(output,source)

    def test_bad_count_rejected(self):
        memory=NS(throw_exception_if_processing_interrupted=lambda:None)
        with patch.dict(sys.modules,{'comfy':NS(model_management=memory),'comfy.model_management':memory}):
            for count in (0,4,737):
                with self.assertRaises(ValueError):GatewayAIHubReferenceFrames().run(torch.zeros(count,2,2,3))
