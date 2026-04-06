# Wasm Integration Guide

本文档说明 Rust Nitro / Wasm 能力在当前 Gateway 架构中的定位，以及如何在不破坏边界的前提下继续扩展。

## 1. 当前定位

Rust 能力当前不是业务编排层，而是基础能力层。它主要负责：

- 输入安全检查。
- Token 统计。
- 敏感规则加载与高性能处理。

这些能力的稳定边界见：

- [BOUNDARY.md](/D:/workspace/codes4/gateway/utils-rust/BOUNDARY.md)

## 2. Go 侧接入原则

Go 侧不应在业务层到处直接拼接 Nitro 调用。当前推荐方式是：

- 通过 dependency facade 暴露稳定方法。
- 在 facade 中集中处理超时、错误映射和降级策略。
- 在 pipeline / application 层只消费语义结果，而不是依赖具体 Nitro 实现细节。

## 3. 扩展新 Wasm 能力时的要求

如果后续新增 Wasm 导出函数或 Rust 能力，请遵守：

- 先确定它是否属于基础能力，而不是业务编排能力。
- 若需要跨语言暴露，优先通过明确契约而不是隐式行为。
- 变更现有稳定语义时，必须同步更新 [BOUNDARY.md](/D:/workspace/codes4/gateway/utils-rust/BOUNDARY.md) 与相关测试。

## 4. 典型错误

下面这些做法会破坏当前边界：

- 让 Rust 直接承担主链路决策。
- 在多个 Go 模块中直接各自接入 Nitro。
- 把不可恢复的策略语义硬编码在 Rust 实现中，却不在 Go 层显式表达。

## 5. 推荐阅读

- [BOUNDARY.md](/D:/workspace/codes4/gateway/utils-rust/BOUNDARY.md)
- [ARCHITECTURE_BOUNDARIES.md](/D:/workspace/codes4/gateway/core-go/ARCHITECTURE_BOUNDARIES.md)
- [SERVICE_CONTRACT.md](/D:/workspace/codes4/gateway/logic-python/SERVICE_CONTRACT.md)
