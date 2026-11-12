import unittest
import numpy as np
from .nr import composite
from . import GatewayVideoStream, GatewayAIHubVideoStream


class NRProfileTests(unittest.TestCase):
    def test_legacy_and_author_order_are_separate(self):
        self.assertFalse(GatewayVideoStream.RTX_FIRST)
        self.assertEqual(GatewayVideoStream.NR_COLOR, 1)
        self.assertTrue(GatewayAIHubVideoStream.RTX_FIRST)
        self.assertEqual(GatewayAIHubVideoStream.NR_COLOR, 0)

    def test_composite_extremes(self):
        original=np.full((3,4,3),[80,110,150],np.uint8)
        enhanced=np.full((3,4,3),[150,100,90],np.uint8)
        np.testing.assert_array_equal(composite(original,enhanced,detail=0,color=0),original)
        np.testing.assert_array_equal(composite(original,enhanced,detail=1,color=1),enhanced)
        preserved=composite(original,enhanced,detail=1,color=0)
        self.assertGreater(int(preserved[0,0,2]),int(preserved[0,0,0]))
        mixed=composite(original,enhanced,detail=.6,color=1)
        self.assertTrue(np.all(mixed>=np.minimum(original,enhanced)))
        self.assertTrue(np.all(mixed<=np.maximum(original,enhanced)))

    def test_black_and_invalid_controls(self):
        black=np.zeros((2,2,3),np.uint8)
        self.assertTrue(np.isfinite(composite(black,black,detail=1,color=0)).all())
        with self.assertRaises(ValueError):composite(black,black,detail=float('nan'),color=0)
