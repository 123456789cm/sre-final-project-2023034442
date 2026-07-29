#!/usr/bin/env bash
set -euo pipefail

OFFLINE_ROOT="${1:-$HOME/offline}"
CTR_NS="k8s.io"

GOLANG_TAR="$OFFLINE_ROOT/sre/golang-alpine.tar"
ALPINE_TAR="$OFFLINE_ROOT/sre/alpine-latest.tar"
CTR_TARGET="docker.io/library/alpine:offline-v1"

for file in "$GOLANG_TAR" "$ALPINE_TAR"; do
    if [[ ! -s "$file" ]]; then
        echo "[ERROR] 文件不存在或为空：$file" >&2
        exit 1
    fi
done

echo "[DOCKER LOAD] builder and runtime base images"

docker load -i "$GOLANG_TAR"
docker load -i "$ALPINE_TAR"

find_docker_image() {
    local suffix="$1"

    docker images \
      --format '{{.Repository}}:{{.Tag}}' |
    awk -v suffix="$suffix" \
      'index($0, suffix) > 0 {print; exit}'
}

prepare_docker_tag() {
    local target="$1"
    local suffix="$2"
    local source=""

    if docker image inspect "$target" >/dev/null 2>&1; then
        echo "[EXISTS] docker $target"
        return 0
    fi

    source=$(find_docker_image "$suffix")

    if [[ -z "$source" ]]; then
        echo "[ERROR] Docker 中找不到镜像：$suffix" >&2
        exit 1
    fi

    echo "[TAG] docker $source -> $target"
    docker tag "$source" "$target"
}

prepare_docker_tag \
  "golang:alpine" \
  "golang:alpine"

prepare_docker_tag \
  "alpine:latest" \
  "alpine:latest"

echo "[CTR PREPARE] faulty-app runtime"

if sudo ctr -n "$CTR_NS" images list -q |
   grep -Fxq "$CTR_TARGET"; then

    echo "[EXISTS] containerd $CTR_TARGET，跳过重复导入"
else
    echo "[IMPORT] $ALPINE_TAR"

    sudo ctr -n "$CTR_NS" images import \
      "$ALPINE_TAR" \
      >/tmp/sre-alpine-import.log

    source=$(
        sudo ctr -n "$CTR_NS" images list -q |
        grep -v '^sha256:' |
        grep -v '@sha256:' |
        awk \
          'index($0, "alpine:latest") > 0 {print; exit}'
    )

    if [[ -z "$source" ]]; then
        echo "[ERROR] 导入后找不到 alpine:latest 镜像" >&2
        cat /tmp/sre-alpine-import.log >&2 || true
        exit 1
    fi

    if ! sudo ctr -n "$CTR_NS" images list -q |
       grep -Fxq "$CTR_TARGET"; then

        echo "[TAG] containerd $source -> $CTR_TARGET"

        sudo ctr -n "$CTR_NS" images tag \
          "$source" \
          "$CTR_TARGET" \
          >/dev/null
    fi
fi

echo
echo "===== 最终检查 ====="

for image in "golang:alpine" "alpine:latest"; do
    if docker image inspect "$image" >/dev/null 2>&1; then
        echo "[PASS] docker $image"
    else
        echo "[FAIL] docker $image"
        exit 1
    fi
done

if sudo ctr -n "$CTR_NS" images list -q |
   grep -Fxq "$CTR_TARGET"; then
    echo "[PASS] containerd $CTR_TARGET"
else
    echo "[FAIL] containerd $CTR_TARGET"
    exit 1
fi

echo "[OK] runtime images are ready"
