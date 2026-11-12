"""Preserve the end of H3 references without changing their 24 FPS timeline."""


class GatewayAIHubReferenceFrames:
    @classmethod
    def INPUT_TYPES(cls):
        return {'required': {'images': ('IMAGE',)}}

    RETURN_TYPES = ('IMAGE', 'INT', 'INT')
    RETURN_NAMES = ('images', 'aligned_count', 'source_count')
    FUNCTION = 'run'
    CATEGORY = 'gateway/video'

    def run(self, images):
        import torch
        import comfy.model_management as memory
        memory.throw_exception_if_processing_interrupted()
        if images.ndim != 4:
            raise ValueError('H3 reference must be an image batch')
        count = images.shape[0]
        if count < 5 or count > 736:
            raise ValueError('H3 reference must contain 5 to 736 frames')
        aligned = count + (5-count) % 17
        if aligned == count:
            return (images, count, count)
        tail = images[-1:].expand(aligned-count, -1, -1, -1)
        padded = torch.cat((images, tail), dim=0)
        memory.throw_exception_if_processing_interrupted()
        return (padded, aligned, count)
