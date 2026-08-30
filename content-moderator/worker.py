import json
import sys
from io import BytesIO

from PIL import Image
from transformers import pipeline

MODEL_NAME = "Falconsai/nsfw_image_detection"
MAX_IMAGE_BYTES = 64 * 1024 * 1024
MAX_IMAGE_PIXELS = 64_000_000
MAX_IMAGE_SIDE = 16_384
CLASSIFIER_IMAGE_SIDE = 1024
CLASSIFIER_SOURCE_SIDE = 2048


def classification_views(source):
    """Return overlapping views so small/local nudity is not lost on resize."""
    image = source.convert("RGB")
    image.thumbnail(
        (CLASSIFIER_SOURCE_SIDE, CLASSIFIER_SOURCE_SIDE),
        Image.Resampling.LANCZOS,
    )
    width, height = image.size
    boxes = (
        (0, 0, width, height),
        (
            int(width * 0.08),
            int(height * 0.08),
            int(width * 0.92),
            int(height * 0.92),
        ),
        (0, 0, width, int(height * 0.72)),
        (0, int(height * 0.28), width, height),
        (
            int(width * 0.12),
            int(height * 0.18),
            int(width * 0.88),
            int(height * 0.82),
        ),
    )
    views = []
    for box in boxes:
        view = image.crop(box)
        view.thumbnail(
            (CLASSIFIER_IMAGE_SIDE, CLASSIFIER_IMAGE_SIDE),
            Image.Resampling.LANCZOS,
        )
        views.append(view)
    return views


def main():
    payload = sys.stdin.buffer.read(MAX_IMAGE_BYTES + 1)
    if not payload or len(payload) > MAX_IMAGE_BYTES:
        raise ValueError("unsupported image size")
    with Image.open(BytesIO(payload)) as source:
        width, height = source.size
        if (
            width <= 0
            or height <= 0
            or width > MAX_IMAGE_SIDE
            or height > MAX_IMAGE_SIDE
            or width * height > MAX_IMAGE_PIXELS
        ):
            raise ValueError("unsupported image dimensions")
        source.draft("RGB", (CLASSIFIER_SOURCE_SIDE, CLASSIFIER_SOURCE_SIDE))
        views = classification_views(source)
    classifier = pipeline("image-classification", model=MODEL_NAME, device=-1)
    predictions = classifier(views, top_k=2, batch_size=1)
    nsfw_score = max(
        next(
            (
                float(item["score"])
                for item in prediction
                if item["label"].lower() == "nsfw"
            ),
            0.0,
        )
        for prediction in predictions
    )
    sys.stdout.write(json.dumps({"nsfw_score": nsfw_score}))


if __name__ == "__main__":
    main()
