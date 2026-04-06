# Gateway

Gateway 是一个多语言 AI 网关，当前由三部分组成：

- `core-go/`：对外 HTTP 与 SSE 入口、请求编排、策略、路由、可观测性与管理接口。
- `logic-python/`：增强能力服务，当前稳定职责是 `CheckInput`、`CheckOutput`、`GetCache`。
- `utils-rust/`：Nitro 基础能力，当前稳定职责是输入安全检查、Token 统计与敏感规则加载。

这套架构已经从“按功能堆叠”收敛为“按边界协作”：Go 负责主链路和装配，Python 与 Rust 通过稳定接口提供可替换能力，而不是渗透到业务编排层。

## 当前架构摘要

主链路在 `core-go` 内部被固定为几层职责：

- `transport`：HTTP / SSE 适配与请求解析。
- `application`：请求编排与用例服务。
- `pipeline`：标准化、策略评估、计划生成、执行、审计。
- `dependencies`：Python / Rust / 其他外部能力的统一门面。
- `bootstrap`：启动、配置、依赖装配、健康状态初始化。

关键实现入口：

- [main.go](/D:/workspace/codes4/gateway/core-go/cmd/gateway/main.go)
- [service.go](/D:/workspace/codes4/gateway/core-go/internal/application/chat/service.go)
- [chat.go](/D:/workspace/codes4/gateway/core-go/internal/handlers/chat.go)
- [chat_pipeline.go](/D:/workspace/codes4/gateway/core-go/internal/pipeline/chat_pipeline.go)
- [facade.go](/D:/workspace/codes4/gateway/core-go/internal/dependencies/facade.go)

## 目录说明

- `core-go/`：Go 网关主体。
- `logic-python/`：Python gRPC 服务与增强逻辑。
- `utils-rust/`：Rust Nitro 与 Wasm 能力。
- `proto/`：跨语言接口定义与兼容性规则。
- `k8s/`：Kubernetes 部署与监控资源。
- `scripts/`：仓库级辅助脚本，例如 proto 生成物同步校验。

## 本地开发

最常用的验证命令：

```powershell
cd core-go
$env:GOCACHE='D:\workspace\codes4\gateway\.gocache'
go test ./...
```

Proto 生成物一致性检查：

```powershell
make proto-check
```

如果需要同时启动整套本地服务，可以使用仓库根目录的脚本：

```powershell
.\run_all.ps1
```

## 文档入口

- 架构边界：[ARCHITECTURE_BOUNDARIES.md](/D:/workspace/codes4/gateway/core-go/ARCHITECTURE_BOUNDARIES.md)
- 开发指南：[DEVELOPER_GUIDE.md](/D:/workspace/codes4/gateway/DEVELOPER_GUIDE.md)
- 代码阅读指南：[CODE_READING_GUIDE.md](/D:/workspace/codes4/gateway/CODE_READING_GUIDE.md)
- 故障排查：[TROUBLESHOOTING_GUIDE.md](/D:/workspace/codes4/gateway/TROUBLESHOOTING_GUIDE.md)
- Kubernetes 部署：[K8S_DEPLOYMENT_GUIDE.md](/D:/workspace/codes4/gateway/K8S_DEPLOYMENT_GUIDE.md)
- 监控接入：[MONITORING.md](/D:/workspace/codes4/gateway/k8s/MONITORING.md)
- Proto 兼容规则：[COMPATIBILITY.md](/D:/workspace/codes4/gateway/proto/COMPATIBILITY.md)
- Python 服务契约：[SERVICE_CONTRACT.md](/D:/workspace/codes4/gateway/logic-python/SERVICE_CONTRACT.md)
- Rust 边界契约：[BOUNDARY.md](/D:/workspace/codes4/gateway/utils-rust/BOUNDARY.md)

## 当前维护结论

当前版本已经具备较强的模块化和较高的可维护性，原因在于：

- 请求编排从 handler 下沉到 application service，边界更稳定。
- 跨语言依赖通过 facade 收口，失败策略不再散落在业务层。
- 配置路径集中在 `Config.Paths`，默认策略文件随仓库提供。
- Proto 契约、文档边界与 CI 自动检查已经建立。

它仍然是一个多语言系统，因此系统级维护成本高于单体 Go 服务，但主链路已经具备清晰、可扩展、可验证的工程结构。
