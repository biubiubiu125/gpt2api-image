FROM python:3.13-slim

WORKDIR /app
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    GPT2API_IMAGE_DATA_DIR=/app/data \
    TZ=Asia/Shanghai \
    REGISTER_EXECUTOR_ADDR=0.0.0.0 \
    REGISTER_EXECUTOR_PORT=8091

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

RUN apt-get update \
    && apt-get install -y --no-install-recommends nodejs npm \
    && npm install --no-save playwright@1.51.1 \
    && npx playwright install --with-deps chromium \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY app.py .
COPY services ./services
COPY utils ./utils

EXPOSE 8091
CMD ["python", "app.py"]
