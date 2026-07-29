# K8s SRE 智能运维助手项目

固定环境：`sre-node1=192.168.30.15`，`ai-node1=192.168.30.100:13533`，命名空间 `sre-demo`。

## 目录

- `main.go`：Go SRE Agent。
- `frontend/index.html`：嵌入式实时仪表盘。
- `k8s/`：命名空间、RBAC、故障应用、告警规则、Agent。
- `argocd/application.yaml`：Gitee GitOps Application。
- `scripts/`：运行时镜像准备、构建导入、仓库地址修改、故障注入和验收。

## 固定平台地址

- Agent 仪表盘：`http://192.168.30.15:30080`
- Grafana：`http://192.168.30.15:31358`
- Prometheus：`http://192.168.30.15:30090`
- Alertmanager：`http://192.168.30.15:30093`

## 首次使用

```bash
chmod +x scripts/*.sh
bash scripts/prepare-runtime-images.sh "$HOME/offline"
```

修改 Gitee 地址：

```bash
bash scripts/set-repo-url.sh <你的Gitee用户名>
```

构建并导入 Agent：

```bash
bash scripts/build-import.sh student-v1
```

部署前检查：

```bash
kubectl kustomize k8s >/tmp/rendered.yaml
kubectl apply --dry-run=server -k k8s
```

推送代码后创建 Argo CD Application：

```bash
kubectl apply -f argocd/application.yaml
```

故障与验收：

```bash
bash scripts/inject-fault.sh
bash scripts/reset-baseline.sh
bash scripts/verify-all.sh
```

## 学生版说明

`main.go` 保留四个 `TODO-STUDENT`。完成四项任务前，`scripts/build-import.sh` 会拒绝构建。学生应 Fork 教师公开模板，并把 `argocd/application.yaml` 的仓库地址改成自己的 Fork。
