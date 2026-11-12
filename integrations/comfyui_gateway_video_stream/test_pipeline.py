"""Real FFmpeg transport with deterministic CPU doubles for the GPU stages."""
from pathlib import Path
from types import SimpleNamespace as NS
from unittest.mock import patch
import sys
import tempfile
import unittest
import numpy as np
import torch
from . import GatewayAIHubVideoStream, GatewayVideoStream


class PipelineTests(unittest.TestCase):
    def test_stage_order_dimensions_and_output(self):
        for cls, expected in ((GatewayAIHubVideoStream,['rtx','fg','nr']),
                              (GatewayVideoStream,['fg','nr','rtx'])):
            with self.subTest(cls=cls.__name__), tempfile.TemporaryDirectory() as directory:
                root=Path(directory); order=[]; dimensions=[]
                class SR:
                    def __init__(self,quality):order.append('rtx')
                    def __enter__(self):return self
                    def __exit__(self,*args):pass
                    def load(self):pass
                    def run(self,image):
                        resized=torch.nn.functional.interpolate(image[None],size=(self.output_height,self.output_width),mode='nearest')[0]
                        return NS(image=resized)
                def interpolate(access,count,width,height,rate,multiplier,scene_threshold,frame_sink):
                    order.append('fg');dimensions.append(('fg',width,height))
                    for index in range(count):
                        rgba,_=access(index)
                        self.assertEqual(rgba.shape,(height,width,4))
                        for _ in range(multiplier if index<count-1 else 1):frame_sink(rgba[...,:3])
                class Session:
                    def __init__(self,layout,options,input_width,input_height,frame_count):
                        order.append('nr');dimensions.append(('nr',input_width,input_height))
                        self.output_width=self.render_width=input_width
                        self.output_height=self.render_height=input_height
                    def __enter__(self):return self
                    def __exit__(self,*args):pass
                    def submit(self,**kwargs):return kwargs['rgba'],kwargs['pts']
                class Guide:
                    def __init__(self,*args,**kwargs):pass
                    def process(self,rgba):return NS(motion=None,reset=True)
                class Bar:
                    def __init__(self,total):self.total=total
                    def update_absolute(self,n):
                        if n>self.total:raise AssertionError('progress overflow')
                fg_type=type('FG',(),{'__module__':'fgstub.nodes'})
                nr_type=type('NR',(),{'__module__':'nrstub.nodes.images'})
                memory=NS(processing_interrupted=lambda:False,throw_exception_if_processing_interrupted=lambda:None,
                          unload_all_models=lambda:None,soft_empty_cache=lambda **kw:None)
                modules={
                    'folder_paths':NS(get_save_image_path=lambda *a:(str(root),'test',0,'',''),get_output_directory=lambda:str(root),get_temp_directory=lambda:str(root/'temp'),get_folder_paths=lambda name:[]),
                    'nodes':NS(NODE_CLASS_MAPPINGS={'RH_DLSS5FrameInterpolation':fg_type,'DLSS5EnhanceImages':nr_type}),
                    'comfy':NS(model_management=memory),'comfy.model_management':memory,'comfy.utils':NS(ProgressBar=Bar),
                    'nvvfx':NS(VideoSuperRes=SR,effects=NS(QualityLevel=NS(HIGH=1))),
                    'fgstub.dlss5nr.fg':NS(interpolate_stream_from_source=interpolate),
                    'nrstub.nodes.common':NS(resolve_layout=lambda settings:None,confirm_feature_18=lambda session,verify:None),
                    'nrstub.dlss5.motion':NS(TemporalGuide=Guide),
                    'nrstub.dlss5.imaging':NS(fit_frame=lambda rgba,w,h:rgba),
                    'nrstub.dlss5.session':NS(DlssSession=Session),
                }
                options=NS(flow_width=64,scene_change_threshold=.15,wants_motion=lambda n:True)
                with patch.dict(sys.modules,modules),patch.object(torch.Tensor,'cuda',lambda self:self):
                    result=cls().run(torch.full((3,64,64,3),.5),{'waveform':torch.zeros(1,1,6000),'sample_rate':48000},
                                     2,True,2,True,'HIGH',19,'test',NS(options=options))
                self.assertEqual(order,expected)
                side=128 if cls.RTX_FIRST else 64
                self.assertEqual(dimensions,[('fg',side,side),('nr',side,side)])
                output=root/result['ui']['videos'][0]['filename']
                self.assertTrue(output.is_file())
                self.assertFalse(list(root.rglob('*.partial.mp4')))
                self.assertFalse(list((root/'temp').iterdir()))
