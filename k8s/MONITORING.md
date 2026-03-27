# AI 网关监控接入说明

仓库中已经包含了将网关指标暴露给 Prometheus Operator 所需的 Kubernetes 资源。

## 涉及文件

- `k8s/servicemonitor.yaml`
  用于抓取 Go 编排层的 `/metrics`
- `k8s/prometheus-rules.yaml`
  定义 readiness、依赖健康和降级事件相关告警
- `k8s/orchestration.yaml`
  为编排层 Service 增加了稳定标签和命名端口 `http`
- `k8s/gateway-all-in-one.yaml`
  在 all-in-one 部署清单中同步了相同标签和命名端口

## 前提条件

这些资源默认依赖 Prometheus Operator 或兼容套件，例如：

- `kube-prometheus-stack`

如果你的集群还不能识别 `ServiceMonitor` 或 `PrometheusRule`，请先安装监控栈。

## 应用顺序

先部署业务服务，再部署监控资源：

```bash
kubectl apply -f k8s/base.yaml
kubectl apply -f k8s/redis.yaml
kubectl apply -f k8s/nitro.yaml
kubectl apply -f k8s/intelligence.yaml
kubectl apply -f k8s/orchestration.yaml
kubectl apply -f k8s/servicemonitor.yaml
kubectl apply -f k8s/prometheus-rules.yaml
```

如果你使用合并部署清单，则顺序为：

```bash
kubectl apply -f k8s/gateway-all-in-one.yaml
kubectl apply -f k8s/servicemonitor.yaml
kubectl apply -f k8s/prometheus-rules.yaml
```

## ServiceMonitor 实际抓取目标

ServiceMonitor 会抓取：

- namespace: `ai-gateway`
- service labels:
  - `app=orchestration`
  - `app.kubernetes.io/name=ai-gateway`
  - `app.kubernetes.io/component=orchestration`
- port: `http`
- path: `/metrics`

## 关键指标

- `gateway_readiness`
  当所有必需依赖健康时为 `1`，否则为 `0`
- `gateway_dependency_health`
  按依赖名称、状态、失败策略、版本暴露健康状态
- `gateway_dependency_required`
  必需依赖为 `1`，可选依赖为 `0`
- `gateway_degraded_events_total`
  记录系统发出的降级事件和 `fail_open_with_audit` 事件

## 已包含的告警

- `AIGatewayNotReady`
  当 `gateway_readiness == 0` 持续 2 分钟触发
- `AIGatewayRequiredDependencyDown`
  当必需依赖持续不健康超过 2 分钟触发
- `AIGatewayOptionalDependencyDegraded`
  当可选依赖持续不健康超过 15 分钟触发
- `AIGatewayDegradedAuditSpike`
  当 10 分钟内降级事件数超过 20 时触发

## 验证方法

```bash
kubectl get svc -n ai-gateway
kubectl describe servicemonitor ai-gateway-orchestration -n ai-gateway
kubectl describe prometheusrule ai-gateway-alerts -n ai-gateway
```

如果集群中 Prometheus Operator 已经正常运行，还应进一步确认：

- Prometheus 已发现该抓取目标
- `/metrics` 中能看到新增指标
- 规则页中已能看到告警规则

## 说明

- 当前告警中的 `runbook_url` 仍是占位地址，建议替换成你自己的内部 runbook 链接
- 如果你的 Prometheus Operator 只选择特定 label 的 `ServiceMonitor` 或 `PrometheusRule`，需要根据集群约定调整这些资源的 metadata labels
