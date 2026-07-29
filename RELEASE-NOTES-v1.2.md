# v1.2 修订说明

- 将平台安装与项目代码彻底分离；项目不再承担 Kubernetes/Argo CD/Prometheus 安装。
- 所有 containerd 精确镜像检查统一使用 `ctr images list -q | grep -Fxq`，不再使用部分版本不存在的 `ctr images inspect`。
- 增加 `prepare-runtime-images.sh`，统一准备 Docker 构建镜像与 faulty-app 的 containerd 标签。
- 增加 `generate-vendor.sh`，支持主机 Go 或本地 `golang:alpine` 容器生成 `go.sum` 和 `vendor/`。
- 增加 `deploy-baseline.sh`、`reset-baseline.sh` 和 platform/baseline/full 三档验收。
- Agent 跳过恢复后短暂残留的旧 Prometheus 告警，避免把已删除 Pod 误判为 Prometheus 不可达。
- Agent 删除旧 Pod 后先确认 Pod 消失，再校验新 Deployment 的 Updated/Ready/Available 状态，避免过早宣告恢复成功。
- Kubernetes Event 按 Pod UID 查询，降低同名对象事件串线风险。
- RBAC 将 `pods/log` 收紧为仅 `get`。
- Gitee 仓库名统一为 `sre-final-project-template`。
- 固定平台端口：Agent 30080、Grafana 31358、Prometheus 30090、Alertmanager 30093。
