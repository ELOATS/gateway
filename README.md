# 多语言 AI 网关 (Polyglot AI Gateway)

这是一个针对企业级需求设计的高性能、多层次 AI 网关。它集成了多模型路由、分布式限流、安全防护、语义缓存及全链路可观测性。

## 🏗 系统架构：三层平面设计

1.  **编排层 (Orchestration - Go):** 系统的唯一入口。负责分布式限流 (Redis)、身份验证、Trace-ID 生成、指标收集及基于 CoW (Copy-on-Write) 的高性能路由调度。
2.  **加速层 (Nitro - Rust):** 处理计算密集型任务。针对 PII 脱敏和 Token 计数实现了分词器与正则的 **Lazy Load** 零成本抽象。
3.  **智能层 (Intelligence - Python):** 处理复杂 AI 逻辑。支持语义缓存及 **异步背离持久化** 机制。

## 🚀 运行步骤
1.  **先决条件:** 确保本地已安装 Redis (默认端口 6379)。
2.  **一键启动 (推荐):** 在根目录运行 `.\run_all.ps1`。
3.  **手动启动 Go 编排层:** `cd core-go && go run ./cmd/gateway`。

## 📂 项目结构 (Go 标准布局)
- **`core-go/`**: 编排层核心。引入 Adapter Factory 模式，支持 OpenTelemetry。
- **`logic-python/`**: 智能层代码。支持向量索引的异步保存。
- **`utils-rust/`**: 加速层代码。优化了资源生命周期管理。
- **`proto/`**: 跨语言 gRPC 定义。
    - `pkg/models/`: 公共数据模型。
    - `api/gateway/v1/`: 生成的 gRPC 客户端代码。
- **`logic-python/`**: 智能层代码。
- **`utils-rust/`**: 加速层代码。
- **`proto/`**: 跨语言 gRPC 定义。
