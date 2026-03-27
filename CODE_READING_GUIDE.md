# 代码阅读入口说明

本文档用于帮助你快速建立对当前代码库的阅读顺序和心智模型。建议第一次阅读时不要按目录“平铺直叙”地看，而是先抓主链路，再回头看路由、安全、观测和部署。

## 1. 先理解系统分层

当前仓库可以先按三层能力来理解：

- `core-go/`
  Go 编排层，是系统的唯一对外入口，负责 HTTP、SSE、策略编排、provider 调用、审计、管理接口和 readiness。
- `utils-rust/`
  Rust Nitro 层，负责同步、安全关键、确定性较强的能力，例如脱敏和 Token 统计。
- `logic-python/`
  Python 智能增强层，负责语义缓存、轻量分类和可降级的增强能力，不应成为主链路正确性的唯一依赖。

如果你只想先读最关键的部分，请优先看 `core-go/`。

## 2. 推荐阅读顺序

推荐按下面顺序进入代码：

1. 网关入口与依赖装配
2. Chat 主链路 pipeline
3. Handler 与路由层
4. 安全与策略相关模块
5. 模型路由模块
6. 观测、审计与运行时状态
7. Python / Rust 边界能力
8. Kubernetes 与监控资源

这样读的好处是：先理解“请求怎么流动”，再理解“每个模块为什么存在”。

## 3. 主链路从哪里开始看

### 3.1 程序入口

先看：

- [main.go](/D:/workspace/codes4/gateway/core-go/cmd/gateway/main.go)

这里主要回答几个问题：

- 系统启动时初始化了哪些依赖
- Nitro、Python、Redis、Router、Observability 是怎么装配进来的
- failure mode 和 readiness 状态是从哪里接入的

### 3.2 HTTP 路由入口

然后看：

- [routes.go](/D:/workspace/codes4/gateway/core-go/internal/routes/routes.go)
- [status.go](/D:/workspace/codes4/gateway/core-go/internal/routes/status.go)

这两处可以帮助你快速理解：

- `/v1/chat/completions` 是如何挂到统一 handler 上的
- `/healthz` 和 `/readyz` 的语义差异
- 为什么 chat 路由现在尽量保持“很薄”

### 3.3 Chat Handler

接着看：

- [chat.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/chat.go)

这个文件建议重点看三个函数：

- `HandleChatCompletions`
- `streamExecute`
- `routeAndExecute`

阅读目标不是记细节，而是先搞清楚：

- handler 负责什么
- handler 明确不再负责什么
- 同步和流式是如何共用统一 pipeline 语义的

### 3.4 统一 Pipeline

最核心的文件是：

- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)

这个文件是当前“第一性原理重构”之后最值得反复看的地方。建议按以下函数顺序看：

1. `Normalize`
2. `EvaluatePolicies`
3. `BuildPlan`
4. `ExecuteSync`
5. `ExecuteStream`
6. `GuardStreamChunk`
7. `RespondDecision`

你会看到热路径现在被收敛成固定阶段：

- 请求标准化
- 策略评估
- 执行计划生成
- provider 调用
- 输出护栏
- 审计与结果返回

如果你只能读一个文件，那就读这个文件。

## 4. 路由系统怎么读

如果你想理解“模型节点是怎么选出来的”，从这里开始：

- [router.go](/D:/workspace/codes4/gateway/core-go/internal/router/router.go)
- [model.go](/D:/workspace/codes4/gateway/core-go/internal/router/model.go)
- [context.go](/D:/workspace/codes4/gateway/core-go/internal/router/context.go)
- [health.go](/D:/workspace/codes4/gateway/core-go/internal/router/health.go)

然后再看不同策略：

- [strategy_weighted.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_weighted.go)
- [strategy_cost.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_cost.go)
- [strategy_latency.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_latency.go)
- [strategy_quality.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_quality.go)
- [strategy_rule.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_rule.go)
- [strategy_fallback.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_fallback.go)

建议先把 `SmartRouter.Route()` 看懂，再去看每个策略的 `Select()`。

## 5. 安全与策略模块怎么读

### 5.1 请求级安全中间件

先看：

- [auth.go](/D:/workspace/codes4/gateway/core-go/internal/middleware/auth.go)
- [requestid.go](/D:/workspace/codes4/gateway/core-go/internal/middleware/requestid.go)
- [tool_auth.go](/D:/workspace/codes4/gateway/core-go/internal/middleware/tool_auth.go)
- [quota.go](/D:/workspace/codes4/gateway/core-go/internal/middleware/quota.go)
- [ratelimit.go](/D:/workspace/codes4/gateway/core-go/internal/middleware/ratelimit.go)

不过要注意：  
这些中间件现在更多承担“入口级保护”和“兼容性职责”，真正的 chat 策略顺序以 pipeline 为准。

### 5.2 Nitro 边界

然后看：

- [client.go](/D:/workspace/codes4/gateway/core-go/internal/nitro/client.go)
- [nitro_service.go](/D:/workspace/codes4/gateway/core-go/internal/nitro/nitro_service.go)
- [wasm_client.go](/D:/workspace/codes4/gateway/core-go/internal/nitro/wasm_client.go)

这几处主要帮助你理解：

- Go 如何只依赖统一接口，不依赖具体载体
- gRPC Nitro 和 Wasm Nitro 在上层语义上必须一致
- Wasm 实例池为什么存在，以及为什么初始化时要预热

## 6. Provider 适配层怎么读

如果你想知道请求最终是怎么打到模型供应商上的，看：

- [provider.go](/D:/workspace/codes4/gateway/core-go/internal/adapters/provider.go)
- [dynamic.go](/D:/workspace/codes4/gateway/core-go/internal/adapters/dynamic.go)
- [loader.go](/D:/workspace/codes4/gateway/core-go/internal/adapters/loader.go)

推荐阅读顺序：

1. `Provider` 接口
2. `NewProvider`
3. `OpenAIAdapter`
4. `DynamicAdapter`
5. `Registry.LoadPlugins`

这样能快速看懂：

- 标准 provider 和动态插件 provider 的差异
- 动态插件目前支持到什么程度
- 为什么流式协议没有直接做成完全泛化

## 7. 观测与运行时状态怎么读

这部分建议一起看：

- [observability.go](/D:/workspace/codes4/gateway/core-go/internal/observability/observability.go)
- [audit_logger.go](/D:/workspace/codes4/gateway/core-go/internal/observability/audit_logger.go)
- [status.go](/D:/workspace/codes4/gateway/core-go/internal/runtime/status.go)
- [admin.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/admin.go)

你会看到三类不同职责：

- 指标、日志、trace
- 合规审计
- 运行时依赖状态与 readiness
- 管理接口与控制面输出

建议重点关注：

- dependency health / readiness 是怎么同步到指标和接口的
- degraded event 是怎么记录的
- `/admin/dependencies` 和 `/readyz` 的职责边界

## 8. Python 和 Rust 从哪里看

### 8.1 Python

先看：

- [main.py](/D:/workspace/codes4/gateway/logic-python/main.py)
- [prompt_injection.py](/D:/workspace/codes4/gateway/logic-python/prompt_injection.py)

推荐关注点：

- 哪些能力是懒加载的
- 哪些能力属于“增强但可降级”
- Qdrant 缓存和 Prompt Injection 检测在 Python 侧扮演什么角色

### 8.2 Rust

先看：

- [lib.rs](/D:/workspace/codes4/gateway/utils-rust/src/lib.rs)
- [slm_scanner.rs](/D:/workspace/codes4/gateway/utils-rust/src/slm_scanner.rs)

推荐关注点：

- Rust 当前真正参与主链路的是哪些函数
- FFI/Wasm 暴露接口如何保持简单和确定性
- `SlmScanner` 为什么还是预研占位层

## 9. 部署与监控从哪里看

如果你想理解本地或 Kubernetes 环境是怎么跑起来的，看：

- [K8S_DEPLOYMENT_GUIDE.md](/D:/workspace/codes4/gateway/K8S_DEPLOYMENT_GUIDE.md)
- [MONITORING.md](/D:/workspace/codes4/gateway/k8s/MONITORING.md)
- [orchestration.yaml](/D:/workspace/codes4/gateway/k8s/orchestration.yaml)
- [servicemonitor.yaml](/D:/workspace/codes4/gateway/k8s/servicemonitor.yaml)
- [prometheus-rules.yaml](/D:/workspace/codes4/gateway/k8s/prometheus-rules.yaml)

阅读目标：

- 怎么把三层服务在本地 Minikube 跑起来
- `/metrics` 如何被采集
- readiness、dependency health、degraded events 如何进入告警

## 10. 新同学第一天建议怎么读

如果是第一次接手这个仓库，建议按这个节奏：

1. 先读 [main.go](/D:/workspace/codes4/gateway/core-go/cmd/gateway/main.go)
2. 再读 [routes.go](/D:/workspace/codes4/gateway/core-go/internal/routes/routes.go)
3. 再读 [chat.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/chat.go)
4. 深读 [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)
5. 补读 [router.go](/D:/workspace/codes4/gateway/core-go/internal/router/router.go)
6. 再回头看 [observability.go](/D:/workspace/codes4/gateway/core-go/internal/observability/observability.go) 和 [status.go](/D:/workspace/codes4/gateway/core-go/internal/runtime/status.go)
7. 最后再看 Python / Rust / K8s

这条路径的优点是：先建立“请求怎么走”的全局图，再进入各个能力模块，不容易迷路。

## 11. 阅读时的几个提醒

- 优先看主链路，不要一开始就扎进测试和部署细节。
- 先抓职责边界，再抓实现细节。
- 看到 middleware 时，要分清“入口保护”与“真正的统一策略阶段”。
- 看到 Python 能力时，默认把它当成“可降级增强层”去理解。
- 看到 Rust 能力时，默认把它当成“同步关键路径工具层”去理解。

如果后续还需要，我可以继续补一版“按场景排查问题的代码入口说明”，例如：

- Chat 请求为什么被拒绝时该看哪里
- 流式输出被中断时该看哪里
- readiness 变红时该看哪里
- provider 路由异常时该看哪里
