# Kubernetes Deployment Guide

本文档说明当前 Gateway 在 Kubernetes 上的推荐部署方式。文档重点放在当前已经稳定的边界，而不是历史版本的部署细节。

## 1. 部署对象

当前系统通常由这些工作负载组成：

- `core-go` 网关服务。
- `logic-python` 增强能力服务。
- Rust Nitro 相关能力，按当前部署方式可能内嵌或作为独立支持组件。
- Redis 等基础依赖。
- 监控与指标采集资源。

## 2. 推荐部署顺序

1. 部署基础配置和 Secret。
2. 部署 Redis 与其他共享依赖。
3. 部署 `logic-python`。
4. 部署 `core-go`。
5. 部署监控资源，如 `ServiceMonitor` 和告警规则。
6. 验证 `/healthz`、`/readyz` 与核心请求路径。

## 3. 配置原则

当前版本需要特别关注这些约束：

- 策略文件必须来自明确路径配置，不能依赖容器当前目录。
- 可降级依赖缺失时，服务应表现为 degraded，而不是直接 panic。
- 启动必需配置应在 Pod 启动阶段暴露错误。
- proto 与客户端生成物必须保持同步，否则可能在运行时出现隐式兼容问题。

## 4. 部署前检查

建议在构建镜像前先执行：

```powershell
cd core-go
$env:GOCACHE='D:\workspace\codes4\gateway\.gocache'
go test ./...
```

以及仓库根目录：

```powershell
make proto-check
```

## 5. 健康检查建议

建议至少配置：

- `livenessProbe` 指向 `/healthz`。
- `readinessProbe` 指向 `/readyz`。
- 如果依赖健康状态参与 readiness，请确认 degraded 场景与业务预期一致。

## 6. 发布后的验证

发布完成后建议检查：

- 网关日志中是否正确输出依赖健康状态。
- `logic-python` 是否能正常响应 `CheckInput`、`CheckOutput`、`GetCache`。
- 若启用监控，Prometheus 是否成功抓取网关指标。
- CI 保护中的 `architecture-guard` 与 `proto-sync` 是否在主分支持续通过。

## 7. 相关文档

- [MONITORING.md](/D:/workspace/codes4/gateway/k8s/MONITORING.md)
- [ARCHITECTURE_BOUNDARIES.md](/D:/workspace/codes4/gateway/core-go/ARCHITECTURE_BOUNDARIES.md)
- [CI_ENFORCEMENT.md](/D:/workspace/codes4/gateway/core-go/CI_ENFORCEMENT.md)
