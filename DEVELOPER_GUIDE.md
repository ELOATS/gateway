# Developer Guide

本文档说明当前 Gateway 的开发边界、扩展方式和日常验证流程。目标是让新增一个策略、Provider、依赖服务或管理接口时，改动范围尽量局限在单一模块与装配层。

## 1. 先理解现在的边界

当前代码已经按职责收口，推荐把 `core-go` 理解为五层：

- `transport`：请求协议适配，主要位于 `internal/handlers` 和 `internal/routes`。
- `application`：用例编排，当前核心是 `internal/application/chat`。
- `pipeline`：主链路阶段化处理，位于 `internal/pipeline`。
- `infrastructure`：Provider、Redis、审计、动态插件、外部连接。
- `bootstrap`：程序入口、配置加载、运行时状态注册，位于 `cmd/gateway`。

不要再把业务编排写回 handler，也不要在 pipeline 里直接拼 gRPC client 与 failure mode 逻辑。

## 2. 关键文件

建议优先熟悉这些入口：

- [main.go](/D:/workspace/codes4/gateway/core-go/cmd/gateway/main.go)
- [bootstrap.go](/D:/workspace/codes4/gateway/core-go/cmd/gateway/bootstrap.go)
- [service.go](/D:/workspace/codes4/gateway/core-go/internal/application/chat/service.go)
- [contracts.go](/D:/workspace/codes4/gateway/core-go/internal/application/chat/contracts.go)
- [chat.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/chat.go)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)
- [policy.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/policy.go)
- [facade.go](/D:/workspace/codes4/gateway/core-go/internal/dependencies/facade.go)
- [config.go](/D:/workspace/codes4/gateway/core-go/internal/config/config.go)

## 3. 扩展规则

### 新增 Provider

只在 adapter / provider 相关目录实现，不要让业务层感知具体供应商类型。

- 维护 Provider 接口兼容性。
- 在装配层注册新 Provider。
- 如需动态插件，校验逻辑放在 adapter loader，而不是 handler 或 pipeline。

### 新增策略

策略属于 policy / pipeline 边界，不要在 Gin middleware 和 pipeline 双处重复实现。

- 输入护栏、工具权限、限流、配额都应统一表达在策略阶段。
- 失败语义要清晰区分 `fail_closed`、`fail_open`、`fail_open_with_audit`。
- 新策略至少补单元测试和一个失败路径测试。

### 新增外部依赖

所有 Python / Rust / gRPC / 远端服务依赖，都要先收口到 dependency facade。

- 业务层只拿稳定接口，不感知网络细节。
- 超时、降级、错误映射都在 facade 里做。
- 如新增 proto 字段，只允许兼容性扩展。

## 4. 配置约束

当前配置由 [config.go](/D:/workspace/codes4/gateway/core-go/internal/config/config.go) 统一加载和归一化。

必须遵守这些规则：

- 外部文件路径只能通过配置注入，不能在模块内部写死默认路径。
- 默认策略文件由仓库提供，当前基线是 [policies.yaml](/D:/workspace/codes4/gateway/core-go/configs/policies.yaml)。
- 启动必需配置缺失时，应在启动阶段失败，而不是在请求链路中途失败。
- 可降级依赖缺失时，应明确进入 degraded，而不是悄悄跳过。

## 5. 测试与验证

基础验证命令：

```powershell
cd core-go
$env:GOCACHE='D:\workspace\codes4\gateway\.gocache'
go test ./...
```

Proto 一致性检查：

```powershell
make proto-check
```

推荐新增代码时至少覆盖：

- 单元测试：router、policy、provider、config、runtime。
- 组件测试：application service、dependency facade。
- 契约测试：proto 与跨语言接口行为。
- 集成测试：主链路、SSE、关键降级场景。

## 6. 文档要求

只要改动了这些边界之一，就应该同步更新对应文档：

- 架构边界：[ARCHITECTURE_BOUNDARIES.md](/D:/workspace/codes4/gateway/core-go/ARCHITECTURE_BOUNDARIES.md)
- CI 保护：[CI_ENFORCEMENT.md](/D:/workspace/codes4/gateway/core-go/CI_ENFORCEMENT.md)
- Proto 兼容：[COMPATIBILITY.md](/D:/workspace/codes4/gateway/proto/COMPATIBILITY.md)
- Python 契约：[SERVICE_CONTRACT.md](/D:/workspace/codes4/gateway/logic-python/SERVICE_CONTRACT.md)
- Rust 契约：[BOUNDARY.md](/D:/workspace/codes4/gateway/utils-rust/BOUNDARY.md)

## 7. 当前不建议做的事

- 不要把新的编排逻辑重新塞回 `ChatHandler`。
- 不要在多个模块重复表达同一个限流或配额规则。
- 不要在业务层直接调用 Python / Rust client。
- 不要绕过 proto 契约直接约定隐式字段语义。
- 不要依赖缺失的本地文件或临时目录来让测试通过。
