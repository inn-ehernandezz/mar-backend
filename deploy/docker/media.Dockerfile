FROM python:3.12-slim-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
      libgl1 libglib2.0-0 tesseract-ocr tesseract-ocr-eng tesseract-ocr-spa \
    && rm -rf /var/lib/apt/lists/*

ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 HF_HOME=/models
WORKDIR /app
COPY workers/media/requirements.txt ./
RUN pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu torch==2.15.0+cpu torchvision==0.29.2+cpu \
    && pip install --no-cache-dir -r requirements.txt
RUN useradd -u 10001 -m mar && mkdir /models && chown mar /models
COPY workers/media/documents.py workers/media/server.py ./
USER mar
EXPOSE 8090
ENTRYPOINT ["python", "server.py"]
