#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR=$(cd "$(dirname "$0")/.." && pwd)
OFFLINE_ROOT=${OFFLINE_ROOT:-$HOME/offline}

# 使用逗号，兼容不同版本 Go。
GOPROXY_VALUE="${GOPROXY:-https://goproxy.cn,direct}"
GOSUMDB_VALUE="${GOSUMDB:-sum.golang.google.cn}"

cd "$PROJECT_DIR"

echo "[INFO] project=$PROJECT_DIR"
echo "[INFO] GOPROXY=$GOPROXY_VALUE"
echo "[INFO] GOSUMDB=$GOSUMDB_VALUE"
echo "[MODE] 固定使用本地 golang:alpine 容器"

# 准备 golang:alpine、alpine:latest 和 faulty-app 镜像。
bash scripts/prepare-runtime-images.sh "$OFFLINE_ROOT"

docker run --rm \
  --network host \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp/go-home \
  -e GOPATH=/tmp/go \
  -e GOCACHE=/tmp/go-build \
  -e GOMODCACHE=/tmp/go/pkg/mod \
  -e GOPROXY="$GOPROXY_VALUE" \
  -e GOSUMDB="$GOSUMDB_VALUE" \
  -e GODEBUG=netdns=go \
  -v "$PROJECT_DIR:/src" \
  -w /src \
  golang:alpine \
  sh -ec '
    mkdir -p \
      "$HOME" \
      "$GOPATH" \
      "$GOCACHE" \
      "$GOMODCACHE"

    echo "===== Go version ====="
    go version

    echo "===== Download and tidy modules ====="
    go mod tidy

    echo "===== Generate vendor ====="
    go mod vendor

    echo "===== Verify vendor build ====="
    go test -mod=vendor ./...
  '

if [[ ! -s go.sum ]]; then
    echo "[ERROR] go.sum 未生成" >&2
    exit 1
fi

if [[ ! -d vendor ]]; then
    echo "[ERROR] vendor 目录未生成" >&2
    exit 1
fi

if [[ ! -s vendor/modules.txt ]]; then
    echo "[ERROR] vendor/modules.txt 未生成" >&2
    exit 1
fi

echo
echo "[PASS] go.sum 已生成"
echo "[PASS] vendor/ 已生成"
echo "[PASS] vendor/modules.txt 已生成"
echo "[OK] Go 依赖准备完成"
