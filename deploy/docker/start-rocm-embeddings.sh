#!/bin/sh
set -eu

python - <<'PY'
import torch

if not torch.version.hip or not torch.cuda.is_available():
    raise SystemExit("No hay una GPU AMD accesible mediante PyTorch ROCm")

sample = torch.ones(8, device="cuda").sum().item()
if sample != 8:
    raise SystemExit("Falló la prueba de cómputo ROCm")
print(f"BGE-M3: ROCm {torch.version.hip} en {torch.cuda.get_device_name(0)}", flush=True)
PY

exec infinity_emb v2 "$@"
