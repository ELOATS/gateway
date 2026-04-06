# Code Reading Guide

本文档给出当前仓库最有效的阅读顺序。重点不是遍历所有目录，而是先抓主链路，再看边界与扩展点。

## 1. 第一遍只看主链路

### 第一步：启动装配

从这里开始：

- [main.go](/D:/workspace/codes4/gateway/core-go/cmd/gateway/main.go)
- [bootstrap.go](/D:/workspace/codes4/gateway/core-go/cmd/gateway/bootstrap.go)

你需要回答的问题：

- 配置从哪里加载。
- Python、Rust、Redis、Router、审计如何装配。
- 运行时状态、健康探针和 degraded 状态如何初始化。

### 第二步：HTTP 与应用层分界

继续看：

- [chat.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/chat.go)
- [service.go](/D:/workspace/codes4/gateway/core-go/internal/application/chat/service.go)
- [contracts.go](/D:/workspace/codes4/gateway/core-go/internal/application/chat/contracts.go)

现在的关键变化是：handler 已经变薄，应用层 service 才是请求编排入口。阅读时要特别区分“协议适配”和“用例编排”。

### 第三步：Pipeline

主链路核心在：

- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)
- [policy.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/policy.go)
- [context.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/context.go)

建议按这个心智模型看：

1. 请求标准化。
2. 策略评估。
3. 执行计划生成。
4. 同步或流式执行。
5. 输出护栏与审计。

## 2. 第二遍看依赖与扩展点

### 跨语言依赖门面

- [facade.go](/D:/workspace/codes4/gateway/core-go/internal/dependencies/facade.go)

这里统一处理：

- Nitro 输入检查。
- Python 输入护栏。
- Python 输出护栏。
- 语义缓存。
- Token 统计。
- 失败模式与错误映射。

### Provider 与路由

- [provider.go](/D:/workspace/codes4/gateway/core-go/internal/adapters/provider.go)
- [loader.go](/D:/workspace/codes4/gateway/core-go/internal/adapters/loader.go)
- [router.go](/D:/workspace/codes4/gateway/core-go/internal/router/router.go)

你要理解的是：Provider 是执行能力，Router 是选择能力，二者都不应该承载业务编排。

## 3. 第三遍看基础设施与约束

### 配置

- [config.go](/D:/workspace/codes4/gateway/core-go/internal/config/config.go)
- [config_test.go](/D:/workspace/codes4/gateway/core-go/internal/config/config_test.go)

重点看路径归一化、默认值和启动时校验。

### 审计与可观测性

- [audit_logger.go](/D:/workspace/codes4/gateway/core-go/internal/observability/audit_logger.go)
- `internal/observability` 目录下其他指标与运行时状态实现。

### Proto 契约

- [gateway.proto](/D:/workspace/codes4/gateway/proto/gateway.proto)
- [gateway_contract_test.go](/D:/workspace/codes4/gateway/core-go/api/gateway/v1/gateway_contract_test.go)
- [COMPATIBILITY.md](/D:/workspace/codes4/gateway/proto/COMPATIBILITY.md)

## 4. 最后再读外部服务

### Python

- [README.md](/D:/workspace/codes4/gateway/logic-python/README.md)
- [SERVICE_CONTRACT.md](/D:/workspace/codes4/gateway/logic-python/SERVICE_CONTRACT.md)

### Rust

- [BOUNDARY.md](/D:/workspace/codes4/gateway/utils-rust/BOUNDARY.md)
- `utils-rust/src` 下的实际实现。

## 5. 现在不需要按旧方式阅读的内容

如果你之前看过旧版文档，请注意这些认知已经过时：

- `ChatHandler` 不再承担完整编排职责。
- 配额不再依赖旧的 `internal/middleware/quota.go`。
- Python 与 Rust 的调用语义不应散落在 pipeline 和 handler 中。
- Proto 契约现在有自动化校验，不能再随意改字段号或 RPC 名称。
