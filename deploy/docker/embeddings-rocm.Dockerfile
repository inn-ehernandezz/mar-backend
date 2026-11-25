FROM python:3.12-slim-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl libdrm-amdgpu1 libelf1 libgomp1 libnuma1 \
    && rm -rf /var/lib/apt/lists/*

ARG ROCM_GPU_ARCH
RUN pip install --no-cache-dir --index-url https://stable.repo.amd.com/rocm/whl-next/ \
      "torch[device-${ROCM_GPU_ARCH}]==2.15.0+rocm10.2.0" \
    && pip install --no-cache-dir 'infinity-emb[server,torch]==0.0.79' 'transformers==4.57.6' \
    && pip check

ENV HF_HOME=/data PYTHONUNBUFFERED=1
RUN useradd -u 10001 -m mar && mkdir /data && chown mar /data
COPY start-rocm-embeddings.sh /usr/local/bin/start-rocm-embeddings.sh
USER mar
EXPOSE 80
ENTRYPOINT ["sh", "/usr/local/bin/start-rocm-embeddings.sh"]
