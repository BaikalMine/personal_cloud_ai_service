import json
import sys
from io import BytesIO

from PIL import Image
from transformers import pipeline

MODEL_NAME = "Falconsai/nsfw_image_detection"


def main():
    image = Image.open(BytesIO(sys.stdin.buffer.read())).convert("RGB")
    classifier = pipeline("image-classification", model=MODEL_NAME, device=-1)
    predictions = classifier(image, top_k=2)
    nsfw_score = next((float(item["score"]) for item in predictions if item["label"].lower() == "nsfw"), 0.0)
    sys.stdout.write(json.dumps({"nsfw_score": nsfw_score}))


if __name__ == "__main__":
    main()
