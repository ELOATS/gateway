# 问题排查入口说明

本文档用于帮助你在出现故障时，快速定位“先看哪里、再看哪里”。  
建议把它和 [CODE_READING_GUIDE.md](/D:/workspace/codes4/gateway/CODE_READING_GUIDE.md) 配合使用：

- `CODE_READING_GUIDE.md` 解决“平时怎么读代码”
- 本文档解决“出问题时先从哪里下手”

## 1. 总体排查顺序

无论遇到什么问题，建议都先按这个顺序排查：

1. 先确认请求有没有进入 Go 网关
2. 再确认请求卡在 pipeline 的哪个阶段
3. 再确认是否涉及 Nitro / Python / Redis / provider 依赖
4. 最后再看部署、Kubernetes 和监控层

优先看这些文件：

- [main.go](/D:/workspace/codes4/gateway/core-go/cmd/gateway/main.go)
- [routes.go](/D:/workspace/codes4/gateway/core-go/internal/routes/routes.go)
- [chat.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/chat.go)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)
- [observability.go](/D:/workspace/codes4/gateway/core-go/internal/observability/observability.go)
- [status.go](/D:/workspace/codes4/gateway/core-go/internal/runtime/status.go)

## 2. Chat 请求直接失败或返回 4xx / 5xx

先看：

- [chat.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/chat.go)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)
- [auth.go](/D:/workspace/codes4/gateway/core-go/internal/middleware/auth.go)

重点排查顺序：

1. 请求是否进入 `HandleChatCompletions`
2. `Normalize` 是否成功解析请求
3. `EvaluatePolicies` 是否提前拒绝
4. `BuildPlan` 是否生成了可执行计划
5. `ExecuteSync` / `ExecuteStream` 是否拿到了 provider 返回

常见对应关系：

- `401/403`
  优先看鉴权、工具权限、策略拒绝路径
- `429`
  优先看限流和配额
- `503`
  优先看必需依赖不可用，例如 Nitro fail-closed
- `500`
  优先看 provider 调用、响应解析或未知执行错误

## 3. 请求被拒绝，但不清楚是谁拒绝的

先看：

- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)
- [audit_logger.go](/D:/workspace/codes4/gateway/core-go/internal/observability/audit_logger.go)

重点关注：

- `PolicyDecision`
- `RespondDecision`
- `RecordExecutionStarted`
- `RecordExecutionCompleted`
- `RecordStreamBlocked`
- `RecordDegraded`

排查思路：

1. 看 `PolicyDecision` 最终是否 `Allow == false`
2. 看 `Reason` 和 `HTTPStatus`
3. 看审计里是否有对应的 reject / blocked / degraded 事件

如果 HTTP 已经拒绝，但审计里没有对应事件，优先检查 pipeline 的拒绝路径有没有统一走 `RespondDecision`。

## 4. 工具调用被拦截，或非工具请求被误伤

先看：

- [tool_auth.go](/D:/workspace/codes4/gateway/core-go/internal/middleware/tool_auth.go)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)

重点关注：

- 请求里是否真的包含 `tools` 或 `tool_choice`
- 前缀探测是否误判
- tool 权限是否来自正确的 `key_label`

如果是“大请求但没有 tools 却被误伤”，重点看：

- 前缀探测逻辑
- 请求体大小阈值
- body 是否被正确恢复给下游

## 5. 限流或配额行为异常

先看：

- [ratelimit.go](/D:/workspace/codes4/gateway/core-go/internal/middleware/ratelimit.go)
- [quota.go](/D:/workspace/codes4/gateway/core-go/internal/middleware/quota.go)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)

常见场景：

- 明明请求不多却频繁 `429`
- Redis 异常后限流表现和预期不一致
- 配额没有扣减或一直不重置

排查重点：

1. 当前走的是 Redis 路径还是本地降级路径
2. `key_label` / `api_key` 是否正确注入
3. Redis 键名是否符合预期
4. 配额是同步拦截失败，还是异步补账失败

如果怀疑是 Redis 故障导致的行为变化，同时看：

- [status.go](/D:/workspace/codes4/gateway/core-go/internal/runtime/status.go)
- `/readyz`
- `/admin/dependencies`

## 6. Nitro 输入护栏异常

先看：

- [client.go](/D:/workspace/codes4/gateway/core-go/internal/nitro/client.go)
- [nitro_service.go](/D:/workspace/codes4/gateway/core-go/internal/nitro/nitro_service.go)
- [wasm_client.go](/D:/workspace/codes4/gateway/core-go/internal/nitro/wasm_client.go)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)

常见症状：

- 请求被错误拦截
- Nitro 不可用时全部 `503`
- Wasm 与 gRPC 表现不一致

排查顺序：

1. 当前 Nitro 用的是哪种载体
2. 初始化时是否预热成功
3. 规则是否已同步到 Wasm 实例
4. failure mode 是否为 `fail_closed`
5. pipeline 是否把错误映射成了预期状态

如果是“服务启动正常，但首个请求才炸”，优先怀疑 Wasm 实例创建或规则同步没有在构造阶段暴露出来。

## 7. Python 增强能力异常

先看：

- [main.py](/D:/workspace/codes4/gateway/logic-python/main.py)
- [prompt_injection.py](/D:/workspace/codes4/gateway/logic-python/prompt_injection.py)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)

常见症状：

- Python 不可用导致缓存 miss 变多
- Prompt Injection 结果异常
- Python 故障后主链路被连带拖垮

排查重点：

1. 当前调用的是输入护栏、输出护栏还是缓存接口
2. 该能力是否属于可降级增强层
3. Go 侧配置的 failure mode 是什么
4. 降级是否被记入 degraded audit / metric

原则上：

- Python 异常不应绕过 Go/Rust 的基础护栏
- Python 异常也不应把本来可降级的主链路直接拖死，除非显式配置成 fail-closed

## 8. 流式输出中断或 SSE 行为异常

先看：

- [chat.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/chat.go)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)

重点看：

- `streamExecute`
- `ExecuteStream`
- `GuardStreamChunk`

常见问题：

- 客户端收到半截响应就结束
- 流式 chunk 被安全护栏打断
- TTFT/TPS 指标异常

排查顺序：

1. provider 流是否真的开始返回 chunk
2. chunk 在写给客户端前是否被 `GuardStreamChunk` 拦截
3. 流中断后是否写入了统一审计事件
4. handler 是否正确关闭了上游流

如果“客户端已经收到内容，但审计里没有记录”，优先看流式中断路径有没有统一走审计封装。

## 9. Provider 路由异常或模型选错

先看：

- [router.go](/D:/workspace/codes4/gateway/core-go/internal/router/router.go)
- [model.go](/D:/workspace/codes4/gateway/core-go/internal/router/model.go)
- [context.go](/D:/workspace/codes4/gateway/core-go/internal/router/context.go)
- [health.go](/D:/workspace/codes4/gateway/core-go/internal/router/health.go)

再按策略类型补看：

- [strategy_weighted.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_weighted.go)
- [strategy_cost.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_cost.go)
- [strategy_latency.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_latency.go)
- [strategy_quality.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_quality.go)
- [strategy_rule.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_rule.go)
- [strategy_fallback.go](/D:/workspace/codes4/gateway/core-go/internal/router/strategy_fallback.go)

排查重点：

1. 候选节点列表是否正确
2. 节点是否被禁用
3. 节点健康状态是否误判
4. 实际命中的策略是什么
5. fallback 是否在主策略失效后接管了结果

## 10. 动态插件 Provider 异常

先看：

- [provider.go](/D:/workspace/codes4/gateway/core-go/internal/adapters/provider.go)
- [dynamic.go](/D:/workspace/codes4/gateway/core-go/internal/adapters/dynamic.go)
- [loader.go](/D:/workspace/codes4/gateway/core-go/internal/adapters/loader.go)

常见问题：

- 插件 YAML 没加载
- Header 模板没渲染成功
- body extra 没注入
- 插件只支持非流式，但上层误走了流式

排查顺序：

1. `GlobalRegistry` 是否加载到了对应插件
2. `PluginName` 是否匹配
3. `BaseURL` 是否被环境配置覆盖
4. 请求头和请求体最终长什么样
5. 是否误用了流式接口

## 11. readiness 变红或依赖状态异常

先看：

- [status.go](/D:/workspace/codes4/gateway/core-go/internal/runtime/status.go)
- [status.go](/D:/workspace/codes4/gateway/core-go/internal/routes/status.go)
- [admin.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/admin.go)
- [observability.go](/D:/workspace/codes4/gateway/core-go/internal/observability/observability.go)

排查顺序：

1. `/healthz` 是否正常
2. `/readyz` 返回的依赖信息是什么
3. `/admin/dependencies` 里哪个依赖变成了 `healthy=false`
4. 它是否是 `required=true`
5. 指标里的 `gateway_dependency_health` 和 `gateway_readiness` 是否一致

如果管理接口显示健康，但指标没更新，优先看 `SystemStatus.Set()` 是否被正确调用。

## 12. 监控告警触发，但不知道从哪看

先看：

- [MONITORING.md](/D:/workspace/codes4/gateway/k8s/MONITORING.md)
- [prometheus-rules.yaml](/D:/workspace/codes4/gateway/k8s/prometheus-rules.yaml)
- [servicemonitor.yaml](/D:/workspace/codes4/gateway/k8s/servicemonitor.yaml)

常见告警对应方向：

- `AIGatewayNotReady`
  优先查 readiness 和 required dependency
- `AIGatewayRequiredDependencyDown`
  优先查具体依赖状态和启动日志
- `AIGatewayOptionalDependencyDegraded`
  优先查 Python、Redis 等可选依赖是否长期异常
- `AIGatewayDegradedAuditSpike`
  优先查 pipeline 中的显式降级事件是否突然增多

## 13. Kubernetes / Minikube 部署异常

先看：

- [K8S_DEPLOYMENT_GUIDE.md](/D:/workspace/codes4/gateway/K8S_DEPLOYMENT_GUIDE.md)
- [orchestration.yaml](/D:/workspace/codes4/gateway/k8s/orchestration.yaml)
- [gateway-all-in-one.yaml](/D:/workspace/codes4/gateway/k8s/gateway-all-in-one.yaml)

建议排查顺序：

1. Pod 是否起来
2. Service 是否暴露
3. 配置和 Secret 是否正确挂载
4. `/healthz`、`/readyz`、`/metrics` 是否可访问
5. ServiceMonitor 是否真的抓到了目标 Service

## 14. 本地开发时先跑哪些验证

### Go

```powershell
cd D:\workspace\codes4\gateway\core-go
$env:GOCACHE='D:\workspace\codes4\gateway\.gocache'
go test ./...
```

### Rust

```powershell
cargo test --manifest-path D:\workspace\codes4\gateway\utils-rust\Cargo.toml
```

### Python

```powershell
python -m py_compile D:\workspace\codes4\gateway\logic-python\main.py D:\workspace\codes4\gateway\logic-python\prompt_injection.py
```

## 15. 最后给一个实战建议

排查时不要一开始就“到处加日志”。

更有效的做法通常是：

1. 先判断问题属于哪一层
2. 找到这层的统一入口
3. 确认是不是走到了预期路径
4. 再检查那条路径上的状态、依赖和配置

如果后续你愿意，我还可以继续补一份更贴近值班场景的文档，例如：

- “5 分钟快速止血手册”
- “按告警名称分类的排障 Runbook”
