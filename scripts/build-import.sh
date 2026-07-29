#!/usr/bin/env bash
set -euo pipefail
TAG=${1:-student-v1}
PROJECT_DIR=$(cd "$(dirname "$0")/.." && pwd)
OFFLINE_ROOT=${OFFLINE_ROOT:-$HOME/offline}
IMAGE="docker.io/library/sre-agent:${TAG}"
ARCHIVE="${PROJECT_DIR}/sre-agent-${TAG}.tar"
cd "$PROJECT_DIR"

if grep -R "TODO-STUDENT" -n main.go; then
  echo "[ERROR] TODO-STUDENT remains in main.go; complete all four tasks first" >&2
  exit 1
fi
[[ -d vendor ]] || { echo "[ERROR] vendor directory is missing" >&2; exit 1; }

bash scripts/prepare-runtime-images.sh "$OFFLINE_ROOT"
GO_VERSION=$(docker run --rm --network=none golang:alpine go version)
echo "[CHECK] builder: $GO_VERSION"

echo "[BUILD] $IMAGE"
docker build --pull=false --network=none -t "$IMAGE" .

echo "[SAVE] $ARCHIVE"
docker save -o "$ARCHIVE" "$IMAGE"

echo "[IMPORT] containerd k8s.io"
sudo ctr -n k8s.io images import "$ARCHIVE" >/tmp/sre-agent-import.log
cat /tmp/sre-agent-import.log

if ! sudo ctr -n k8s.io images list -q | grep -Fxq "$IMAGE"; then
  source=$(sudo ctr -n k8s.io images list -q | grep -v '@sha256:' | awk -v s="sre-agent:${TAG}" 'index($0,s)>0 {print; exit}')
  [[ -n "$source" ]] || { echo "[ERROR] imported SRE Agent image not found" >&2; exit 1; }
  sudo ctr -n k8s.io images tag "$source" "$IMAGE" >/dev/null
fi

echo "[UPDATE] k8s/40-agent.yaml -> $IMAGE"
sed -i -E "s#image: docker.io/library/sre-agent:[A-Za-z0-9._-]+#image: ${IMAGE}#" k8s/40-agent.yaml
sudo ctr -n k8s.io images list -q | grep -Fxq "$IMAGE"
echo "[OK] $IMAGE is available to Kubernetes"
