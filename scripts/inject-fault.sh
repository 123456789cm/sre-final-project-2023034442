#!/usr/bin/env bash
set -euo pipefail
kubectl get configmap fault-switch -n sre-demo >/dev/null
kubectl patch configmap fault-switch -n sre-demo --type merge -p '{"data":{"FAIL_MODE":"true"}}'
kubectl delete pod -n sre-demo -l app=faulty-app --wait=false
printf '[OK] fault injected\nObserve with: watch -n 2 kubectl get pods -n sre-demo\n'
