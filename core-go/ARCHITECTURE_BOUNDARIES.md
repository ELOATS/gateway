# Architecture Boundaries

本文档定义当前 `core-go` 的稳定边界。它不是“理想架构草图”，而是当前实现已经承诺维护的职责划分。

## 1. 总体分层

当前 `core-go` 可按以下几层理解：

- `transport`
- `application`
- `pipeline`
- `dependencies`
- `bootstrap`

这几层的目标是：把协议、用例、阶段化流程、外部依赖和启动装配拆开，避免任何一层重新长成新的“万能协调器”。

## 2. 各层职责

### transport

主要位于 `internal/handlers` 与 `internal/routes`。

职责：

- 解析 HTTP / SSE 请求。
- 做协议层返回。
- 处理与 Gin 相关的输入输出细节。

不负责：

- 业务编排。
- 跨语言依赖调用细节。
- 降级策略决策。

### application

当前主要位于 `internal/application/chat`。

职责：

- 表达用例入口。
- 组织主链路执行顺序。
- 在 handler 和 pipeline 之间形成稳定接口。

不负责：

- 具体 gRPC / Redis / Nitro 调用。
- 具体 Provider 实现。

### pipeline

主要位于 `internal/pipeline`。

职责：

- 请求标准化。
- 策略评估。
- 计划生成。
- 同步与流式执行。
- 输出护栏、审计与结果收口。

要求：

- 主链路阶段要保持清晰。
- 不直接散落外部 client 调用逻辑。
- 不与 Gin 强耦合。

### dependencies

主要位于 `internal/dependencies`。

职责：

- 统一访问 Python 与 Rust 能力。
- 屏蔽超时、错误映射、降级与失败模式差异。
- 为 application / pipeline 提供稳定语义接口。

这层是跨语言复杂度的主要隔离带。

### bootstrap

主要位于 `cmd/gateway`。

职责：

- 加载配置。
- 初始化运行态。
- 装配 Router、Provider、Facade、Observability 等依赖。
- 启动 HTTP 服务。

不负责：

- 承载请求链路业务规则。
- 演化成“第二个 pipeline”。

## 3. 配置边界

配置统一由 [config.go](/D:/workspace/codes4/gateway/core-go/internal/config/config.go) 加载。

必须遵守：

- 外部文件路径只能由配置注入。
- 默认资源应随仓库提供。
- 启动必需配置在启动阶段校验。
- 可降级依赖要显式表达 degraded，而不是隐式跳过。

## 4. 扩展边界

### Provider

Provider 属于基础设施或 adapter 边界。业务层不应该识别具体 provider 类型。

### Policy

限流、配额、输入护栏、工具权限等都应统一表达在策略阶段，避免 middleware 和 pipeline 双实现。

### 外部依赖

所有 Python / Rust / 远端能力都应先进入 dependency facade，再进入业务链路。

## 5. 当前禁止的回退

- 不要把业务编排重新写回 handler。
- 不要让 pipeline 直接拼外部 client 与 failure mode。
- 不要在多个位置重复表达同一个规则。
- 不要绕过配置系统写死路径或默认文件。
