from contextlib import asynccontextmanager
from io import BytesIO

from fastapi import FastAPI, HTTPException, Request
from PIL import Image, UnidentifiedImageError
from transformers import pipeline

MODEL_NAME = "Falconsai/nsfw_image_detection"
classifier = None


@asynccontextmanager
async def lifespan(_: FastAPI):
    global classifier
    classifier = pipeline("image-classification", model=MODEL_NAME, device=-1)
    yield


app = FastAPI(lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)


@app.get("/healthz")
async def healthz():
    if classifier is None:
        raise HTTPException(status_code=503, detail="model is loading")
    return {"ok": True}


@app.post("/classify")
async def classify(request: Request):
    content_type = request.headers.get("content-type", "").split(";", 1)[0].lower()
    if content_type not in {"image/jpeg", "image/png", "image/webp"}:
        raise HTTPException(status_code=415, detail="unsupported image type")
    body = await request.body()
    if not body or len(body) > 64 * 1024 * 1024:
        raise HTTPException(status_code=413, detail="unsupported image size")
    try:
        image = Image.open(BytesIO(body)).convert("RGB")
    except (UnidentifiedImageError, OSError) as error:
        raise HTTPException(status_code=400, detail="invalid image") from error
    predictions = classifier(image, top_k=2)
    nsfw_score = next((float(item["score"]) for item in predictions if item["label"].lower() == "nsfw"), 0.0)
    return {"nsfw_score": nsfw_score}
