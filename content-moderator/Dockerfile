FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HF_HOME=/models

RUN adduser --disabled-password --gecos "" --uid 10001 appuser
WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu torch==2.7.1
RUN pip install --no-cache-dir -r requirements.txt
COPY app.py ./
RUN mkdir -p /models && chown -R appuser:appuser /app /models
USER appuser
EXPOSE 8080
ENTRYPOINT ["python", "-m", "uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8080"]
