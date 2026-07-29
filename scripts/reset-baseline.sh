#!/usr/bin/env bash
set -euo pipefail
kubectl patch configmap fault-switch -n sre-demo --type merge -p '{"data":{"FAIL_MODE":"false"}}'
kubectl delete pod -n sre-demo -l app=faulty-app --wait=false || true
kubectl rollout status deployment/faulty-app -n sre-demo --timeout=5m
kubectl get configmap fault-switch -n sre-demo -o jsonpath='{.data.FAIL_MODE}{"\n"}'
