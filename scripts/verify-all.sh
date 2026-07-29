#!/usr/bin/env bash
set -euo pipefail
MODE=${1:-full}
fail=0
check() {
  local name=$1; shift
  if "$@" >/tmp/sre-check.out 2>/tmp/sre-check.err; then
    printf '[PASS] %s\n' "$name"
  else
    printf '[FAIL] %s\n' "$name"
    sed -n '1,10p' /tmp/sre-check.err
    fail=1
  fi
}

check 'Kubernetes node Ready' bash -c "kubectl get node sre-node1 -o jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}' | grep -qx True"
check 'Argo CD pods Running' bash -c "kubectl get pods -n argocd --no-headers | awk '{print \$3}' | grep -Ev 'Running|Completed' | grep -q . && exit 1 || exit 0"
check 'Argo CD CRDs complete' bash -c 'kubectl get crd applications.argoproj.io applicationsets.argoproj.io appprojects.argoproj.io >/dev/null'
check 'Helm release deployed' bash -c "helm status prometheus-stack -n monitoring | grep -q '^STATUS: deployed$'"
check 'Monitoring pods Running' bash -c "kubectl get pods -n monitoring --no-headers | awk '{print \$3}' | grep -Ev 'Running|Completed' | grep -q . && exit 1 || exit 0"
check 'Grafana NodePort 31358' bash -c "kubectl get svc prometheus-stack-grafana -n monitoring -o jsonpath='{.spec.ports[0].nodePort}' | grep -qx 31358"
check 'Prometheus NodePort 30090' bash -c "kubectl get svc prometheus-stack-kube-prom-prometheus -n monitoring -o jsonpath='{.spec.ports[?(@.port==9090)].nodePort}' | grep -qx 30090"
check 'Alertmanager NodePort 30093' bash -c "kubectl get svc prometheus-stack-kube-prom-alertmanager -n monitoring -o jsonpath='{.spec.ports[?(@.port==9093)].nodePort}' | grep -qx 30093"
check 'LLM endpoint reachable' curl -fsS --max-time 10 http://192.168.30.100:13533/v1/models

if [[ "$MODE" == platform ]]; then
  [[ $fail -eq 0 ]] || { echo '[RESULT] platform verification failed'; exit 1; }
  echo '[RESULT] platform checks passed'
  exit 0
fi

check 'Faulty app deployment available' bash -c "kubectl get deploy faulty-app -n sre-demo -o jsonpath='{.status.availableReplicas}' | grep -Eq '^[1-9]'"
check 'Fault switch is false' bash -c "kubectl get cm fault-switch -n sre-demo -o jsonpath='{.data.FAIL_MODE}' | grep -qx false"
check 'PrometheusRule exists' kubectl get prometheusrule pod-crash-alert -n monitoring

if [[ "$MODE" == baseline ]]; then
  [[ $fail -eq 0 ]] || { echo '[RESULT] baseline verification failed'; exit 1; }
  echo '[RESULT] SRE-00 baseline checks passed'
  exit 0
fi

check 'Argo CD Application Synced/Healthy' bash -c "kubectl get application sre-final-project -n argocd -o jsonpath='{.status.sync.status} {.status.health.status}' | grep -qx 'Synced Healthy'"
check 'SRE Agent deployment available' bash -c "kubectl get deploy sre-agent -n sre-demo -o jsonpath='{.status.availableReplicas}' | grep -Eq '^[1-9]'"
check 'Dashboard NodePort is 30080' bash -c "kubectl get svc sre-agent-service -n sre-demo -o jsonpath='{.spec.ports[0].nodePort}' | grep -qx 30080"
check 'Dashboard health endpoint' curl -fsS --max-time 10 http://192.168.30.15:30080/healthz

if [[ $fail -ne 0 ]]; then
  echo '[RESULT] verification failed'
  exit 1
fi
echo '[RESULT] all core checks passed'
