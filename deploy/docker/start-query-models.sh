#!/usr/bin/env bash
# Cargado desde start-documents.sh, que detecta los dispositivos y prepara BGE-M3.

query_gpu_memory() {
    case "$1" in
        nvidia)
            local gpu_probe=(docker run --rm --network none)
            if [[ "$runtime" == nvidia ]]; then
                gpu_probe+=(--runtime=nvidia)
            fi
            "${gpu_probe[@]}" --gpus "device=$nvidia_device" -e NVIDIA_DRIVER_CAPABILITIES=utility \
                ubuntu:24.04 nvidia-smi --query-gpu=memory.total,memory.free --format=csv,noheader,nounits |
                awk -F, 'NR == 1 {print $1+0, $2+0}'
            ;;
        amd)
            docker run --rm --network none --device "$amd_device" ubuntu:24.04 sh -ec '
                device="/sys/class/drm/${1##*/}/device"
                total=$(cat "$device/mem_info_vram_total")
                used=$(cat "$device/mem_info_vram_used")
                printf "%s %s\n" "$((total / 1048576))" "$(((total - used) / 1048576))"
            ' sh "$amd_device"
            ;;
    esac
}

start_query_gpu() {
    local service=$1 vendor=$2 budget=$3 device info total free existing fraction
    case "$vendor" in
        nvidia) device=$nvidia_device ;;
        amd) device=$amd_device ;;
    esac
    [[ -n "$device" ]] || return 1

    if ! info=$(query_gpu_memory "$vendor"); then
        printf '%s: no se pudo consultar la VRAM de %s.\n' "$service" "$vendor" >&2
        return 1
    fi
    read -r total free <<< "$info"
    if [[ ! "$total" =~ ^[1-9][0-9]*$ || ! "$free" =~ ^[0-9]+$ ]]; then
        return 1
    fi

    local id
    id=$("${compose[@]}" --profile query-gpu ps -a -q "$service")
    existing=
    if [[ -n "$id" ]]; then
        existing=$(docker inspect --format '{{index .Config.Labels "mar.gpu.device"}} {{.State.Health.Status}}' "$id") || existing=
    fi
    if [[ "$existing" != "$device healthy" ]] && (( free < budget + 512 )); then
        printf '%s: %s tiene %d MiB disponibles, se necesitan %d MiB.\n' "$service" "$vendor" "$free" "$((budget + 512))"
        return 1
    fi

    fraction=$(awk -v budget="$budget" -v total="$total" 'BEGIN {printf "%.5f", budget / total}')
    printf '%s: probando %s en %s (%d MiB libres).\n' "$service" "$vendor" "$device" "$free"
    if MAR_RAG_GPU_DEVICE="$device" MAR_RAG_GPU_MEMORY_FRACTION="$fraction" \
        "${compose[@]}" -f "compose.queries.$vendor.yaml" up -d --no-deps --wait --wait-timeout "$query_gpu_timeout" "$service"; then
        return 0
    fi
    "${compose[@]}" --profile query-gpu stop "$service"
    return 1
}

start_query_models() {
    local query_gpu_timeout=${MAR_RAG_GPU_START_TIMEOUT:-180}
    if [[ ! "$query_gpu_timeout" =~ ^[1-9][0-9]*$ ]]; then
        printf 'MAR_RAG_GPU_START_TIMEOUT debe ser un entero positivo en segundos.\n' >&2
        return 2
    fi

    export MAR_MODEL_QWEN3_GPU_URL=
    export MAR_MODEL_RERANKER_GPU_URL=
    export MAR_MODEL_BGE_M3_CPU_URL=

    # These services are already running when the API has to fall back mid-request.
    "${compose[@]}" up -d --no-deps --wait --wait-timeout "$timeout" qwen3 reranker
    if "$embedding_on_gpu"; then
        "${compose[@]}" up -d --no-deps --wait --wait-timeout "$timeout" embeddings-cpu
        export MAR_MODEL_BGE_M3_CPU_URL=http://embeddings-cpu:80
    else
        "${compose[@]}" --profile query-backup stop embeddings-cpu
    fi

    if [[ "$query_accelerator" != cpu ]] &&
        { start_query_gpu qwen3-gpu nvidia 6656 || start_query_gpu qwen3-gpu amd 6656; }; then
        export MAR_MODEL_QWEN3_GPU_URL=http://qwen3-gpu:8000
    else
        "${compose[@]}" --profile query-gpu stop qwen3-gpu
        printf 'Qwen3: CPU.\n'
    fi

    if [[ "$query_accelerator" != cpu ]] &&
        { start_query_gpu reranker-gpu nvidia 3072 || start_query_gpu reranker-gpu amd 3072; }; then
        export MAR_MODEL_RERANKER_GPU_URL=http://reranker-gpu:8000
    else
        "${compose[@]}" --profile query-gpu stop reranker-gpu
        printf 'Reranker: CPU.\n'
    fi

    if [[ "$query_accelerator" == gpu ]] &&
        [[ -z "$MAR_MODEL_QWEN3_GPU_URL" || -z "$MAR_MODEL_RERANKER_GPU_URL" ]]; then
        printf 'El modo gpu requiere que Qwen3 y el reranker arranquen en GPU.\n' >&2
        return 1
    fi
}
