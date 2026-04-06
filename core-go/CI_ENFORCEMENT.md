# CI Enforcement

本文档说明当前 CI 在架构层面保护什么，以及这些检查为什么存在。

## 1. 当前关键检查

CI 当前重点保护两类内容：

- 关键边界是否持续存在。
- `proto/gateway.proto` 与已提交生成物是否保持同步。

对应工作流位于：

- [ci.yml](/D:/workspace/codes4/gateway/.github/workflows/ci.yml)

## 2. architecture-guard

`architecture-guard` 会验证这些关键文档和资源存在：

- [policies.yaml](/D:/workspace/codes4/gateway/core-go/configs/policies.yaml)
- [ARCHITECTURE_BOUNDARIES.md](/D:/workspace/codes4/gateway/core-go/ARCHITECTURE_BOUNDARIES.md)
- [COMPATIBILITY.md](/D:/workspace/codes4/gateway/proto/COMPATIBILITY.md)
- [SERVICE_CONTRACT.md](/D:/workspace/codes4/gateway/logic-python/SERVICE_CONTRACT.md)
- [BOUNDARY.md](/D:/workspace/codes4/gateway/utils-rust/BOUNDARY.md)
- [gateway_contract_test.go](/D:/workspace/codes4/gateway/core-go/api/gateway/v1/gateway_contract_test.go)

这类检查的目的不是“保证文档存在就行”，而是避免架构边界在长期演化中被静默删除。

## 3. proto-sync

`proto-sync` 会校验：

- `proto/gateway.proto` 的当前描述。
- Go 已提交生成物。
- Python 已提交生成物。

当前使用的校验入口包括：

- `make proto-check`
- [verify_proto_sync.py](/D:/workspace/codes4/gateway/scripts/verify_proto_sync.py)
- [main.go](/D:/workspace/codes4/gateway/core-go/cmd/proto_descriptor/main.go)

## 4. 为什么这类保护重要

多语言系统最容易退化的地方不是单个模块代码，而是边界失真：

- 文档还写旧结构，代码已经换了。
- proto 改了，但生成物没同步。
- 外部服务契约已经变化，调用侧还按旧语义运行。

CI 的作用就是把这些“长期腐化点”尽量提前到提交阶段暴露。

## 5. 本地对应命令

```powershell
cd core-go
$env:GOCACHE='D:\workspace\codes4\gateway\.gocache'
go test ./...
```

```powershell
make proto-check
```

如果你修改了契约、边界或关键资源，请在提交前先跑一遍。
