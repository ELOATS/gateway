# Troubleshooting Guide

本文档聚焦当前版本的主链路、配置和跨语言依赖边界，帮助快速定位问题发生在哪一层。

## 1. 先判断是哪一类问题

常见故障可以先分成四类：

- 启动失败：配置缺失、策略文件路径错误、依赖初始化失败。
- 请求失败：输入护栏、路由、Provider、输出护栏或审计阶段报错。
- 降级异常：可选依赖不可用时没有按预期进入 degraded 或 `fail_open`。
- 契约漂移：proto、Python 生成物、Go 生成物或跨语言行为不一致。

## 2. 启动失败排查

优先检查：

- [config.go](/D:/workspace/codes4/gateway/core-go/internal/config/config.go)
- [policies.yaml](/D:/workspace/codes4/gateway/core-go/configs/policies.yaml)
- [main.go](/D:/workspace/codes4/gateway/core-go/cmd/gateway/main.go)

重点确认：

- 策略文件路径是否来自配置，而不是当前工作目录碰巧命中。
- 启动必需依赖是否存在。
- 可降级依赖缺失时是否被正确标记为 degraded。

## 3. 请求链路故障排查

建议按这个顺序看日志与代码：

1. `ChatHandler` 是否正确收到了请求。
2. application service 是否成功构建执行上下文。
3. pipeline 在哪一个阶段失败。
4. dependency facade 是否把外部错误正确映射回来。
5. Provider 或 Router 是否返回了可解释错误。

关键文件：

- [chat.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/chat.go)
- [service.go](/D:/workspace/codes4/gateway/core-go/internal/application/chat/service.go)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)
- [facade.go](/D:/workspace/codes4/gateway/core-go/internal/dependencies/facade.go)

## 4. 输入与输出护栏问题

如果怀疑问题出在 Python / Rust 边界，先对照：

- [SERVICE_CONTRACT.md](/D:/workspace/codes4/gateway/logic-python/SERVICE_CONTRACT.md)
- [BOUNDARY.md](/D:/workspace/codes4/gateway/utils-rust/BOUNDARY.md)

重点确认：

- Nitro 输入检查是否先执行。
- Python `CheckInput` 是否按预期参与或被降级。
- Python `CheckOutput` 是否对同步和流式路径都生效。
- 当前策略是 `fail_closed` 还是 `fail_open_with_audit`。

## 5. 测试不稳定或 CI 失败

优先运行：

```powershell
cd core-go
$env:GOCACHE='D:\workspace\codes4\gateway\.gocache'
go test ./...
```

然后检查：

```powershell
make proto-check
```

如果是 CI 上 `architecture-guard` 或 `proto-sync` 失败，通常意味着：

- 关键边界文档被删除或遗漏。
- `gateway.proto` 已改动，但 Go 或 Python 生成物没有同步。
- 契约测试与实现语义不再匹配。

参考文档：

- [CI_ENFORCEMENT.md](/D:/workspace/codes4/gateway/core-go/CI_ENFORCEMENT.md)
- [COMPATIBILITY.md](/D:/workspace/codes4/gateway/proto/COMPATIBILITY.md)

## 6. 监控与运行态问题

如果服务能启动但表现异常，继续排查：

- `/healthz` 与 `/readyz` 是否语义正确。
- 审计日志是否成功关闭，是否存在句柄泄漏。
- 关键依赖健康状态是否体现在运行态指标中。

同时配合阅读：

- [MONITORING.md](/D:/workspace/codes4/gateway/k8s/MONITORING.md)
- [K8S_DEPLOYMENT_GUIDE.md](/D:/workspace/codes4/gateway/K8S_DEPLOYMENT_GUIDE.md)

## 7. 当前最有用的排查原则

- 先定位边界，再定位实现。
- 先确认配置和契约，再怀疑业务逻辑。
- 对跨语言问题优先看 facade，不要直接从深层 gRPC 调用开始追。
- 如果一个规则同时出现在 middleware 和 pipeline，优先怀疑职责重复。
