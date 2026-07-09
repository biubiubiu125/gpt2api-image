FROM python:3.13-slim-bookworm

WORKDIR /app
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    GPT2API_IMAGE_DATA_DIR=/app/data \
    TZ=Asia/Shanghai \
    REGISTER_EXECUTOR_ADDR=0.0.0.0 \
    REGISTER_EXECUTOR_PORT=8091

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py .
COPY services ./services
COPY utils ./utils

EXPOSE 8091
CMD ["python", "app.py"]
