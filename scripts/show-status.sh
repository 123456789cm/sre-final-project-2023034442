#!/usr/bin/env bash
set -o pipefail

# ── helpers ──
PASS()  { printf "  [\033[32mPASS\033[0m] %s\n" "$*"; }
FAIL()  { printf "  [\033[31mFAIL\033[0m] %s\n" "$*"; }
INFO()  { printf "  [INFO]  %s\n" "$*"; }
SEP()   { printf -- "----------------------------------------\n"; }

PROMETHEUS_URL="http://prometheus-stack-kube-prom-prometheus.monitoring.svc.cluster.local:9090"
LLM_URL="http://192.168.30.100:13533"
DASHBOARD_URL="http://localhost:8080"
AGENT_NS="sre-demo"
FAILS=0

SEP
printf "  SRE Agent 一键状态汇总\n"
printf "  当前时间: %s\n" "$(date '+%F %T')"
SEP

printf "\n── 1. Kubernetes 节点状态 ──\n"
NODE_STATUS=$(kubectl get nodes -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
if [ "$NODE_STATUS" = "True" ]; then
  PASS "节点 Ready"
else
  FAIL "节点异常: $NODE_STATUS"
  ((FAILS++))
fi

printf "\n── 2. Argo CD Application ──\n"
APP_STATUS=$(kubectl get application sre-final-project -n argocd -o jsonpath='{.status.sync.status}{" / "}{.status.health.status}' 2>/dev/null)
if [ "$APP_STATUS" = "Synced / Healthy" ]; then
  PASS "Application: $APP_STATUS"
else
  FAIL "Application: $APP_STATUS"
  ((FAILS++))
fi

printf "\n── 3. fault-switch ──\n"
FAIL_MODE=$(kubectl get configmap fault-switch -n "$AGENT_NS" -o jsonpath='{.data.FAIL_MODE}' 2>/dev/null)
if [ "$FAIL_MODE" = "false" ]; then
  PASS "FAIL_MODE = false (正常)"
else
  FAIL "FAIL_MODE = $FAIL_MODE (异常)"
  ((FAILS++))
fi

printf "\n── 4. Pod 状态 ──\n"
FAULTY_POD=$(kubectl get pods -n "$AGENT_NS" -l app=faulty-app -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
FAULTY_STATUS=$(kubectl get pods -n "$AGENT_NS" -l app=faulty-app -o jsonpath='{.items[0].status.phase}' 2>/dev/null)
if [ "$FAULTY_STATUS" = "Running" ]; then
  PASS "faulty-app: $FAULTY_POD ($FAULTY_STATUS)"
else
  FAIL "faulty-app: $FAULTY_POD ($FAULTY_STATUS)"
  ((FAILS++))
fi

AGENT_POD=$(kubectl get pods -n "$AGENT_NS" -l app=sre-agent -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
AGENT_POD_STATUS=$(kubectl get pods -n "$AGENT_NS" -l app=sre-agent -o jsonpath='{.items[0].status.phase}' 2>/dev/null)
if [ "$AGENT_POD_STATUS" = "Running" ]; then
  PASS "sre-agent: $AGENT_POD ($AGENT_POD_STATUS)"
else
  FAIL "sre-agent: $AGENT_POD ($AGENT_POD_STATUS)"
  ((FAILS++))
fi

printf "\n── 5. Agent 当前镜像 ──\n"
AGENT_IMAGE=$(kubectl get deploy sre-agent -n "$AGENT_NS" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)
INFO "$AGENT_IMAGE"

printf "\n── 6. Prometheus Ready ──\n"
PROM_READY=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$PROMETHEUS_URL/-/ready" 2>/dev/null)
if [ "$PROM_READY" = "200" ]; then
  PASS "Prometheus Ready (HTTP $PROM_READY)"
else
  FAIL "Prometheus 不可达 (HTTP $PROM_READY)"
  ((FAILS++))
fi

printf "\n── 7. LLM 可达状态 ──\n"
LLM_CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$LLM_URL/v1/models" 2>/dev/null)
if [ "$LLM_CODE" = "200" ]; then
  PASS "LLM 可达 (HTTP $LLM_CODE)"
else
  FAIL "LLM 不可达 (HTTP $LLM_CODE)"
  ((FAILS++))
fi

printf "\n── 8. Agent /api/status ──\n"
AGENT_STATUS_JSON=$(curl -s --max-time 5 "$DASHBOARD_URL/api/status" 2>/dev/null)
if [ -n "$AGENT_STATUS_JSON" ]; then
  AGENT_STATE=$(echo "$AGENT_STATUS_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('agentStatus','unknown'))" 2>/dev/null)
  PASS "Agent 状态: $AGENT_STATE"
  LAST_ACTION_OK=$(echo "$AGENT_STATUS_JSON" | python3 -c "import sys,json; d=json.load(sys.stdin); a=d.get('lastAction'); print('yes' if a and a.get('success') else 'no')" 2>/dev/null)
  INFO "最近行动结果: $( [ "$LAST_ACTION_OK" = "yes" ] && echo '成功' || echo '未执行/失败' )"
else
  FAIL "无法获取 Agent /api/status"
  ((FAILS++))
fi

printf "\n"
SEP
if [ "$FAILS" -eq 0 ]; then
  printf "  [\033[32mRESULT\033[0m] 所有检查通过，系统健康\n"
else
  printf "  [\033[31mRESULT\033[0m] %d 项检查失败\n" "$FAILS"
fi
SEP

exit $FAILS
