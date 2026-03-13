# 多语言 AI 网关 (Polyglot AI Gateway)

这是一个针对企业级需求设计的高性能、多层次 AI 网关。它集成了多模型路由、安全防护、语义缓存及全链路可观测性。

## 🏗 系统架构：三层平面设计

1.  **编排层 (Orchestration - Go):** 系统的唯一入口。负责身份验证、Trace-ID 生成、指标收集及请求调度。
2.  **加速层 (Nitro - Rust):** 处理计算密集型任务（PII 脱敏、Token 计数）。
3.  **智能层 (Intelligence - Python):** 处理复杂 AI 逻辑（语义缓存、安全审计）。

## 🚀 运行步骤
1.  **一键启动 (推荐):** 在根目录运行 `.\run_all.ps1`。
2.  **手动启动 Go 编排层:** `cd core-go && go run ./cmd/gateway`。

## 📂 项目结构 (Go 标准布局)
- **`core-go/`**: 编排层核心。
    - `cmd/gateway/`: 程序入口。
    - `internal/`: 私有业务逻辑（适配器、路由、处理器、中间件）。
    - `pkg/models/`: 公共数据模型。
    - `api/gateway/v1/`: 生成的 gRPC 客户端代码。
- **`logic-python/`**: 智能层代码。
- **`utils-rust/`**: 加速层代码。
- **`proto/`**: 跨语言 gRPC 定义。
