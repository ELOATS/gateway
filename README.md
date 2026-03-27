# 多语言 AI 网关

这是一个由三层平面组成的 AI 网关系统：

- Go 编排层
- Rust Nitro 安全与 Token 工具层
- Python 智能增强与缓存层

目前系统已经按“第一性原理”收敛了主链路，核心特点是：

- 统一的请求标准化入口
- 统一的策略决策点
- 统一的执行计划
- 显式区分降级与 fail-closed
- 显式暴露依赖健康状态、版本和失败策略

## 目录结构

- `core-go/`
  Go 编排层，包含 HTTP 入口、策略管线、路由、可观测性、管理接口和控制台
- `logic-python/`
  Python 智能层，负责语义缓存和可选增强能力
- `utils-rust/`
  Rust Nitro 层，负责同步安全能力和 Token 工具能力
- `proto/`
  跨语言 gRPC 协议
- `k8s/`
  Kubernetes 部署、监控资源和相关文档

## 当前系统能力

- Chat 请求已经收敛为统一的 Go pipeline，而不是散落在 middleware 和 handler 中
- 工具鉴权、限流、配额、输入护栏、路由、输出护栏、审计都走统一热路径
- 运行时依赖状态支持显式暴露：
  - readiness
  - health
  - version
  - failure mode
- Prometheus 指标已新增：
  - `gateway_readiness`
  - `gateway_dependency_health`
  - `gateway_dependency_required`
  - `gateway_degraded_events_total`
- Kubernetes 监控资源已新增：
  - `k8s/servicemonitor.yaml`
  - `k8s/prometheus-rules.yaml`

## 本地开发

常用本地验证命令：

```powershell
cd core-go
$env:GOCACHE='D:\workspace\codes4\gateway\.gocache'
go test ./...
```

如果要本地直接启动整套服务，可以使用：

```powershell
.\run_all.ps1
```

## Kubernetes 与 Minikube

本地 Minikube 分步部署文档：

- [K8S_DEPLOYMENT_GUIDE.md](/D:/workspace/codes4/gateway/K8S_DEPLOYMENT_GUIDE.md)

监控接入说明：

- [MONITORING.md](/D:/workspace/codes4/gateway/k8s/MONITORING.md)

## 监控接入摘要

如果你在 Kubernetes 中使用 Prometheus Operator：

1. 先部署网关工作负载
2. 再应用 `k8s/servicemonitor.yaml`
3. 再应用 `k8s/prometheus-rules.yaml`
4. 最后确认 orchestration service 的 `/metrics` 已被采集

## 验证

当前 Go 编排层改动已通过：

```powershell
cd core-go
$env:GOCACHE='D:\workspace\codes4\gateway\.gocache'
go test ./...
```
