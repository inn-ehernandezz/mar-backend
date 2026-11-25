#!/usr/bin/env bash
set -euo pipefail

scope=$1
shift
compose=("$@")
accelerator=${MAR_DOCUMENT_ACCELERATOR:-auto}
timeout=${MAR_DOCUMENT_START_TIMEOUT:-600}

case "$scope" in
    all|models) ;;
    *) printf 'Alcance de arranque desconocido: %s\n' "$scope" >&2; exit 2 ;;
esac
case "$accelerator" in
    auto|cpu|gpu) ;;
    *) printf 'MAR_DOCUMENT_ACCELERATOR debe ser auto, cpu o gpu.\n' >&2; exit 2 ;;
esac
if [[ ! "$timeout" =~ ^[1-9][0-9]*$ ]]; then
    printf 'MAR_DOCUMENT_START_TIMEOUT debe ser un entero positivo en segundos.\n' >&2
    exit 2
fi

docker_info=$(docker info --format '{{.Architecture}} {{if index .Runtimes "nvidia"}}nvidia{{end}}')
read -r architecture runtime <<< "$docker_info"
registry=ghcr.io/huggingface/text-embeddings-inference
cpu_tag=cpu-1.9
if [[ "$architecture" == aarch64 || "$architecture" == arm64 ]]; then
    cpu_tag=cpu-arm64-1.9
fi

nvidia_tag_for() {
    if [[ "$architecture" == aarch64 || "$architecture" == arm64 ]]; then
        [[ "$1" == 12.1 ]] && printf '%s\n' '121-1.9'
        return
    fi
    case "$1" in
        7.5) printf '%s\n' 'turing-1.9' ;;
        8.0) printf '%s\n' '1.9' ;;
        8.6) printf '%s\n' '86-1.9' ;;
        8.9) printf '%s\n' '89-1.9' ;;
        9.0) printf '%s\n' 'hopper-1.9' ;;
        10.0) printf '%s\n' '100-1.9' ;;
        12.0) printf '%s\n' '120-1.9' ;;
        *) return 1 ;;
    esac
}

nvidia_tag=
nvidia_device=
if [[ "$accelerator" != cpu ]] &&
        { [[ "$accelerator" == gpu || "$runtime" == nvidia ]] || command -v nvidia-smi >/dev/null; }; then
    probe=(docker run --rm --network none)
    if [[ "$runtime" == nvidia ]]; then
        probe+=(--runtime=nvidia)
    fi
    if gpu_list=$("${probe[@]}" --gpus all \
        -e NVIDIA_DRIVER_CAPABILITIES=compute,utility ubuntu:24.04 \
        nvidia-smi --query-gpu=uuid,compute_cap,memory.free --format=csv,noheader,nounits 2>&1); then
        most_free=-1
        while IFS=, read -r uuid capability memory; do
            capability=${capability//[[:space:]]/}
            memory=${memory//[[:space:]]/}
            if [[ "$uuid" == GPU-* && "$memory" =~ ^[0-9]+$ ]] &&
                    tag=$(nvidia_tag_for "$capability") && (( memory > most_free )); then
                most_free=$memory
                nvidia_tag=$tag
                nvidia_device=$uuid
            fi
        done <<< "$gpu_list"
    elif [[ "$accelerator" == gpu ]]; then
        printf '%s\n' "$gpu_list" >&2
    fi
fi

amd_device=
if [[ "$accelerator" != cpu && ( "$architecture" == x86_64 || "$architecture" == amd64 ) ]] &&
        { [[ -c /dev/kfd ]] || [[ "$accelerator" == gpu ]]; }; then
    # Match AMD render nodes to ROCm compute targets and prefer more VRAM.
    if amd_info=$(docker run --rm --network none --device /dev/kfd --device /dev/dri ubuntu:24.04 \
        sh -ec '
          best=-1
          selected=
          for node in /dev/dri/renderD*; do
              [ -c "$node" ] || continue
              device="/sys/class/drm/${node##*/}/device"
              vendor=$(cat "$device/vendor" 2>/dev/null) || continue
              [ "$vendor" = 0x1002 ] || continue
              for properties in /sys/class/kfd/kfd/topology/nodes/*/properties; do
                  [ -r "$properties" ] || continue
                  minor=$(sed -n "s/^drm_render_minor //p" "$properties")
                  [ "$minor" = "${node##*renderD}" ] || continue
                  version=$(sed -n "s/^gfx_target_version //p" "$properties")
                  [ "${version:-0}" -gt 0 ] || continue
                  vram=$(cat "$device/mem_info_vram_total" 2>/dev/null) || vram=0
                  [ "$vram" -gt "$best" ] || continue
                  best=$vram
                  selected=$node
                  target=$(printf "gfx%d%d%x" "$((version / 10000))" "$((version / 100 % 100))" "$((version % 100))")
              done
          done
          [ -n "$selected" ]
          printf "%s %s %s %s\n" "$selected" "$(stat -c %g "$selected")" "$(stat -c %g /dev/kfd)" "$target"
        ' 2>/dev/null); then
        read -r amd_device amd_render_group amd_kfd_group amd_target <<< "$amd_info"
    fi
fi

if [[ "$accelerator" == gpu && -z "$nvidia_tag" && -z "$amd_device" ]]; then
    printf 'El modo gpu requiere una GPU NVIDIA o AMD compatible y visible desde Docker.\n' >&2
    exit 1
fi

build=(media)
if [[ "$scope" == all ]]; then
    build+=(api)
fi
export MAR_BGE_M3_IMAGE="$registry:$cpu_tag"
"${compose[@]}" build "${build[@]}"
"${compose[@]}" up -d --wait --wait-timeout "$timeout" media

selected_compose=()

if [[ -n "$nvidia_tag" ]]; then
    export MAR_BGE_M3_IMAGE="$registry:$nvidia_tag"
    export MAR_DOCUMENT_GPU_DEVICE="$nvidia_device"
    gpu_compose=("${compose[@]}" -f compose.documents.gpu.yaml)
    printf 'BGE-M3: probando GPU NVIDIA %s con %s.\n' "$nvidia_device" "$MAR_BGE_M3_IMAGE"
    if "${gpu_compose[@]}" up -d --wait --wait-timeout "$timeout" embeddings; then
        selected_compose=("${gpu_compose[@]}")
    else
        printf 'BGE-M3 no arrancó con NVIDIA.\n' >&2
    fi
fi

if (( ${#selected_compose[@]} == 0 )) && [[ -n "$amd_device" ]]; then
    export MAR_DOCUMENT_AMD_RENDER="$amd_device"
    export MAR_DOCUMENT_AMD_RENDER_GROUP="$amd_render_group"
    export MAR_DOCUMENT_AMD_KFD_GROUP="$amd_kfd_group"
    export MAR_DOCUMENT_AMD_GFX="$amd_target"
    amd_compose=("${compose[@]}" -f compose.documents.amd.yaml)
    printf 'BGE-M3: probando GPU AMD %s (%s) con ROCm.\n' "$amd_device" "$amd_target"
    if "${amd_compose[@]}" build embeddings &&
        "${amd_compose[@]}" up -d --no-build --wait --wait-timeout "$timeout" embeddings; then
        selected_compose=("${amd_compose[@]}")
    else
        printf 'BGE-M3 no arrancó con AMD ROCm.\n' >&2
    fi
fi

if (( ${#selected_compose[@]} == 0 )); then
    if [[ "$accelerator" == gpu ]]; then
        printf 'No se pudo arrancar BGE-M3 en GPU.\n' >&2
        exit 1
    fi
    export MAR_BGE_M3_IMAGE="$registry:$cpu_tag"
    selected_compose=("${compose[@]}")
    printf 'BGE-M3: arrancando en CPU con %s.\n' "$MAR_BGE_M3_IMAGE"
    "${selected_compose[@]}" up -d --wait --wait-timeout "$timeout" embeddings
fi

if [[ "$scope" == all ]]; then
    "${selected_compose[@]}" up -d --wait --wait-timeout "$timeout"
fi
