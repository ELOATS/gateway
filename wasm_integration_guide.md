# NITRO 2.0 (Wasm): 全栈集成与高性能学习指南

本指南旨在帮助您理解 NITRO 2.0 是如何利用 WebAssembly (Wasm) 彻底消除 gRPC 带来的进程间通讯 (IPC) 损耗，并实现“算法本地化、安全隔离化”的架构目标。

## 1. 为什么选择 Wasm？

在 NITRO 1.0 中，我们通过 gRPC 连接 Rust 模块。虽然逻辑清晰，但存在以下痛点：
- **通讯开销**: 每次脱敏都需要经过 Protobuf 序列化、TCP/Socket 传输、反序列化。对于高并发网关，这会增加 ~2-5ms 的固定延迟。
- **部署复杂性**: 必须同时启动 Go 进程和独立的 Rust Service。
- **资源浪费**: 每个进程都有自己的内存基数。

**NITRO 2.0 (Wasm)** 实现了：
- **零通讯延迟**: Rust 代码被编译为字节码，直接在 Go 进程的虚拟机中运行，调用开销几乎等同于本地函数。
- **安全沙箱**: Wasm 无法直接访问 Host 的文件系统或网络，所有交互必须通过显式的内存映射，安全性极高。

## 2. 核心架构：Go-Rust 内存桥接

Wasm 虚拟机与宿主 (Go) 之间是 **内存隔离** 的。它们只有唯一的通信渠道：**线性内存 (Linear Memory)**。

### 交互序列 (FFI 链路)
1. **[Go] 申请空间**: 在 Wasm 内存中调用 `malloc` 申请 $N$ 字节。
2. **[Go] 写入参数**: 将 Prompt 字符串写入上述地址。
3. **[Go] 发起调用**: 传递地址（指针/数字）给 Wasm 的 `check_input_wasm`。
4. **[Rust] 执行算法**: 从内存读取数据，执行脱敏正则。
5. **[Rust] 返回指针**: 在 Rust 堆上生成结果字符串，向 Go 返回该结果的新地址。
6. **[Go] 读取结果**: 从 Wasm 内存地址读取处理后的文本。
7. **[Go] 释放内存**: 调用 `free_string` 让 Rust 回收该部分资源（**防止内存泄漏的关键**）。

## 3. 演进路径：如何一步步嵌入独立进程？

将 Rust 从独立 gRPC 进程迁移至 Go 内部，我们遵循了以下六个标准化步骤：

### 第一步：抽象统一接口 (Interface Abstraction)
在 Go 侧定义 `NitroClient` 接口，包含 `CheckInput` 和 `CountTokens` 方法。
- **目的**: 确保业务代码（如 `ChatHandler`）不需要关心底层是通过 gRPC 还是 Wasm 实现的，实现**多态切换**。

### 第二步：核心逻辑脱钩 (Logic Neutralization)
重构 Rust 的 `lib.rs`，移除所有 `tokio`、`tonic` (gRPC) 以及网络、文件 IO 依赖。
- **注意**: Wasm 环境下很难处理异步（Async）和系统调用。我们将算法逻辑抽离为纯粹的“输入字符串 -> 处理 -> 输出字符串”模式。

### 第三步：Wasm 编译打通 (Target Compilation)
使用 `wasm32-unknown-unknown` 目标进行编译。
- **命令**: `cargo build --target wasm32-unknown-unknown --release`
- **经验**: 避免使用 `wasm32-wasip1` 如果您的逻辑不依赖复杂的系统调用，这样产出的 `.wasm` 模块体积更小且更容易在主流运行时中加载。

### 第四步：引入 Wazero 运行时 (Runtime Hosting)
在 Go 侧引入 `github.com/tetratelabs/wazero`。
- **操作**: 创建 `WasmNitroClient` 实现 `NitroClient` 接口，负责加载 `.wasm` 文件并初始化虚拟机。

### 第五步：实现内存桥接 (Linear Memory Bridge)
这是最关键的工程环节。
- **Rust 侧**: 导出 `malloc` 和 `free` 供外部管理内存。
- **Go 侧**: 将 Go 字符串拷贝进 Wasm 线性内存，并在函数返回后处理地址转换。

### 第六步：实现热切换与回落 (Fallback Logic)
在 `main.go` 启动时，优先尝试加载本地 `nitro.wasm`。
- **策略**: 如果 Wasm 初始化成功，则使用 `WasmNitroClient`；否则自动退避至 `GrpcNitroClient` 连接已有的 Rust 独立服务。这确保了系统的 **100% 向后兼容性**。

## 4. 快速学习路径

### 🦀 Rust 侧 (Provider)
查看 [lib.rs](file:///d:/workspace/codes4/gateway/utils-rust/src/lib.rs) 中的 `no_mangle` 导出函数。
- **重点**: 理解 `std::mem::forget` 的作用——它告诉 Rust “不要帮我管理这段内存了，我已经把它借给了别人（Host）”。

### 🐹 Go 侧 (Host)
查看 [wasm_client.go](file:///d:/workspace/codes4/gateway/core-go/internal/observability/wasm_client.go) 中的 `wazero` 初始化。
- **重点**: 理解 `Memory().Read()` 逻辑。Wasm 返回的是一个数值地址，我们需要把这个地址指向的字节流捞回来转换成 Go String。

## 4. 最佳实践建议

1. **单次申请，多次使用**: 如果脱敏规则不常变化，在启动时一次性调用 `set_sensitive_rules_wasm`，利用 `arc-swap` 在 Wasm 内部实现原子重载。
2. **严格的内存闭环**: 任何时候只要 Wasm 函数返回了指针（指针不等于数字，它是对内存的所有权），Host 端必须在 `defer` 中执行 `free_string`。
3. **WASI 支持**: 虽然我们的算法不依赖外部 IO，但在编译时开启 `wasi` 支持可以显著增强模块的生存能力，方便未来接入 SLM (Small Language Models) 的本地文件读取。

---
> [!IMPORTANT]
> NITRO 2.0 标志着网关架构从“分布式松耦合”向“即时单体加速”的进化。通过这种方式，我们既保留了 Rust 的极速算法优势，又避免了分布式计算带来的额外复杂度。
