# Proto Compatibility

本文档定义 `proto/gateway.proto` 的兼容性规则。目标是让 Go、Python 与后续其他语言实现都能基于稳定契约协作。

## 1. 基本原则

对外已经被消费的 proto 语义，默认视为兼容性承诺。修改时优先选择“增量扩展”，不要选择“重写现有语义”。

## 2. 允许的变更

- 为现有 message 新增字段。
- 在不改变既有含义的前提下补充注释与文档。
- 新增向后兼容的 RPC 或 message。
- 为调用方未使用的保留扩展点补充实现。

## 3. 不允许的变更

- 修改既有字段号。
- 删除已被使用的字段而不保留兼容策略。
- 修改既有 RPC 名称。
- 改变现有请求或响应字段的语义。
- 通过“保持字段不变但偷换含义”的方式制造隐式 breaking change。

## 4. 当前保护机制

当前兼容性依赖这些护栏：

- [gateway_contract_test.go](/D:/workspace/codes4/gateway/core-go/api/gateway/v1/gateway_contract_test.go)
- [verify_proto_sync.py](/D:/workspace/codes4/gateway/scripts/verify_proto_sync.py)
- [ci.yml](/D:/workspace/codes4/gateway/.github/workflows/ci.yml)

## 5. 变更流程建议

如果必须修改 `gateway.proto`：

1. 先确认是否能通过新增字段或新增 RPC 完成目标。
2. 更新 Go 与 Python 生成物。
3. 运行 `make proto-check`。
4. 如语义边界发生变化，同步更新 Python / Rust / Go 侧对应契约文档。

## 6. 当前最重要的实践

proto 是跨语言边界的一部分，不是某个单独服务的内部实现文件。任何改动都应按公共接口来审视。
