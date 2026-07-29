#!/usr/bin/env bash
set -euo pipefail
PROJECT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$PROJECT_DIR"
kubectl apply -f k8s/00-namespace.yaml
kubectl apply -f k8s/10-rbac.yaml
kubectl apply -f k8s/20-fault-app.yaml
kubectl apply -f k8s/30-prometheus-rule.yaml
kubectl rollout status deployment/faulty-app -n sre-demo --timeout=5m
kubectl get configmap fault-switch -n sre-demo -o jsonpath='{.data.FAIL_MODE}{"\n"}'
