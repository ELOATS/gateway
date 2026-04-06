# logic-python

`logic-python` 是 Gateway 的增强能力服务，不承担主链路编排职责。Go 网关通过 gRPC 调用它，以获得可替换、可降级的增强能力。

## 当前稳定职责

根据当前契约，Python 服务只负责三类能力：

- `CheckInput`
- `CheckOutput`
- `GetCache`

对应更正式的语义约束见：

- [SERVICE_CONTRACT.md](/D:/workspace/codes4/gateway/logic-python/SERVICE_CONTRACT.md)

## 为什么单独存在

Python 适合承载需要灵活模型库、语义处理或实验迭代的增强逻辑，但这些能力不能直接侵入 Go 的业务编排层。因此当前设计是：

- Go 负责稳定主链路。
- Python 负责增强能力。
- 失败策略由 Go 端 dependency facade 统一控制。

## 与 Go 的关系

Go 端不会在业务层直接散落 gRPC 调用。所有对 Python 的调用都应通过：

- [facade.go](/D:/workspace/codes4/gateway/core-go/internal/dependencies/facade.go)

这保证了超时、降级、错误映射和审计策略都能统一管理。

## 本地开发

如果需要在本地运行 Python 服务，请先同步依赖并确保 proto 生成物最新：

```powershell
uv sync
uv run python -m grpc_tools.protoc -I ../proto --python_out=. --grpc_python_out=. ../proto/gateway.proto
```

仓库级一致性校验：

```powershell
uv run python ../scripts/verify_proto_sync.py
```

## 不应做的事

- 不要在 Python 侧引入新的隐式 RPC 语义而不更新 proto。
- 不要把主链路正确性建立在 Python 必定可用的前提上，除非该能力被明确声明为必需依赖。
- 不要绕过 [SERVICE_CONTRACT.md](/D:/workspace/codes4/gateway/logic-python/SERVICE_CONTRACT.md) 修改现有 RPC 语义。
