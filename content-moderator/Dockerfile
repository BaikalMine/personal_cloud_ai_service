FROM python:3.12-slim@sha256:09f7da3bc104798d0afb40bc08d23ab2da20a76130cec1f2ef170848f5d85217

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HF_HOME=/models

RUN adduser --disabled-password --gecos "" --uid 10001 appuser
WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu torch==2.13.0
RUN python -m pip install --no-cache-dir --upgrade pip==26.2.1 setuptools==83.0.0
RUN pip install --no-cache-dir -r requirements.txt
COPY app.py worker.py ./
RUN mkdir -p /models && chown -R appuser:appuser /app /models
USER appuser
EXPOSE 8080
ENTRYPOINT ["python", "-m", "uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8080"]
