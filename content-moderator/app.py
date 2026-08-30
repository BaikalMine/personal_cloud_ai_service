from contextlib import asynccontextmanager
from io import BytesIO
import json
import subprocess
import sys

from fastapi import FastAPI, HTTPException, Request
from PIL import Image, UnidentifiedImageError


MAX_IMAGE_BYTES = 64 * 1024 * 1024
MAX_IMAGE_PIXELS = 64_000_000
MAX_IMAGE_SIDE = 16_384


@asynccontextmanager
async def lifespan(_: FastAPI):
    yield


app = FastAPI(lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)


@app.get("/healthz")
async def healthz():
    return {"ok": True, "model_loaded": False}


@app.post("/classify")
async def classify(request: Request):
    content_type = request.headers.get("content-type", "").split(";", 1)[0].lower()
    if content_type not in {"image/jpeg", "image/png", "image/webp"}:
        raise HTTPException(status_code=415, detail="unsupported image type")
    body = await request.body()
    if not body or len(body) > MAX_IMAGE_BYTES:
        raise HTTPException(status_code=413, detail="unsupported image size")
    try:
        with Image.open(BytesIO(body)) as image:
            width, height = image.size
            if (
                width <= 0
                or height <= 0
                or width > MAX_IMAGE_SIDE
                or height > MAX_IMAGE_SIDE
                or width * height > MAX_IMAGE_PIXELS
            ):
                raise HTTPException(status_code=413, detail="unsupported image dimensions")
            image.verify()
    except (UnidentifiedImageError, OSError, SyntaxError) as error:
        raise HTTPException(status_code=400, detail="invalid image") from error
    try:
        worker = subprocess.run(
            [sys.executable, "worker.py"],
            input=body,
            capture_output=True,
            timeout=70,
            check=False,
        )
    except subprocess.TimeoutExpired as error:
        raise HTTPException(status_code=504, detail="classification timed out") from error
    if worker.returncode != 0:
        raise HTTPException(status_code=500, detail="classification worker failed")
    try:
        result = json.loads(worker.stdout)
        nsfw_score = float(result["nsfw_score"])
    except (KeyError, TypeError, ValueError, json.JSONDecodeError) as error:
        raise HTTPException(status_code=500, detail="invalid classification response") from error
    return {"nsfw_score": nsfw_score}
