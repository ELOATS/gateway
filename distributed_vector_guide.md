# Distributed Vector Guide

本文档说明仓库中与向量缓存、语义命中和分布式部署相关的设计约束。当前版本不把“分布式向量系统”作为独立主链路，而把它视为 Python 增强能力的一部分。

## 1. 当前定位

语义缓存相关能力当前由 `logic-python` 通过 `GetCache` 提供。Go 网关只通过稳定接口消费这一能力，不直接依赖具体向量库或嵌入实现。

这意味着：

- 向量检索是增强能力，不是 Go 主链路的领域模型。
- 语义缓存失败时，行为由 Go 端降级策略决定。
- 分布式部署策略应优先隐藏在 Python 服务内部，而不是泄漏到 Go 编排层。

## 2. 设计原则

如果要把向量能力扩展到更大规模，应遵守这些规则：

- 保持 `GetCache` 的外部语义稳定。
- 不要让 Go 感知具体向量引擎实现差异。
- 命中与未命中、部分错误、超时等语义必须清晰映射回 facade。
- 如果引入分片、副本或远端向量数据库，这些都应属于 Python 服务内部实现细节。

## 3. 当前建议

- 把缓存键语义、模型区分和命中阈值管理在 Python 服务内部。
- 把超时、可用性与 fail-open / fail-closed 策略保留在 Go 侧。
- 对于跨语言排查，先看 `GetCache` 契约，再看具体向量引擎。

## 4. 与主架构的关系

向量能力并不拥有请求主链路的控制权。当前主链路仍然以 Go 的 application service 与 pipeline 为中心，而不是以 Python 检索为中心。这一点在维护时必须保持。

更多约束请参考：

- [SERVICE_CONTRACT.md](/D:/workspace/codes4/gateway/logic-python/SERVICE_CONTRACT.md)
- [ARCHITECTURE_BOUNDARIES.md](/D:/workspace/codes4/gateway/core-go/ARCHITECTURE_BOUNDARIES.md)
