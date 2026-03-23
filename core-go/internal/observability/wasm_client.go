package observability

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// WasmNitroClient 是基于 Wazero 的本地 Wasm 驱动实现。
// 
// 【学习背景】: Wazero 是 Go 语言编写的无依赖 Wasm 运行时。
// 它允许我们加载 .wasm 字节码并在 Go 进程内像调用普通函数一样执行 Rust 编写的高性能算法。
type WasmNitroClient struct {
	runtime wazero.Runtime // Wasm 运行时环境
	module  api.Module     // 已实例化的 Wasm 模块实例
	mu      sync.Mutex     // FFI 并发锁：虽然 Wasm 内部通常无状态，但为了防止内存管理竞态，建议串行 FFI 交互。

	// 缓存 Wasm 导出的 FFI 句柄，避免每次 Call 时的查找开销。
	malloc      api.Function
	freePtr     api.Function
	setRules    api.Function
	countTokens api.Function
	checkInput  api.Function
	freeString  api.Function
}

// NewWasmNitroClient 初始化并加载 Wasm 模块。
func NewWasmNitroClient(ctx context.Context, wasmPath string, sensitiveRules string) (*WasmNitroClient, error) {
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("读取 Wasm 文件失败: %w", err)
	}

	// 1. 创建运行时。
	r := wazero.NewRuntime(ctx)
	
	// 2. 挂载 WASI 支持：虽然我们的算法是纯计算，但 Rust 编译器默认会链接部分 WASI 存根，必须支持。
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	// 3. 实例化模块。
	mod, err := r.InstantiateWithConfig(ctx, wasmBytes, wazero.NewModuleConfig().WithStartFunctions())
	if err != nil {
		r.Close(ctx)
		return nil, fmt.Errorf("实例化 Wasm 模块失败: %w", err)
	}

	client := &WasmNitroClient{
		runtime:     r,
		module:      mod,
		malloc:      mod.ExportedFunction("malloc"),
		freePtr:     mod.ExportedFunction("free_ptr"),
		setRules:    mod.ExportedFunction("set_sensitive_rules_wasm"),
		countTokens: mod.ExportedFunction("count_tokens_wasm"),
		checkInput:  mod.ExportedFunction("check_input_wasm"),
		freeString:  mod.ExportedFunction("free_string"),
	}

	// 4. 初次注入敏感词规则。
	if sensitiveRules != "" {
		if err := client.SyncRules(ctx, sensitiveRules); err != nil {
			return nil, fmt.Errorf("同步脱敏规则至 Wasm 失败: %w", err)
		}
	}

	return client, nil
}

// CheckInput 调用 Wasm 实现进程内脱敏。
func (c *WasmNitroClient) CheckInput(ctx context.Context, prompt string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// A. 将参数写入 Wasm 内存：
	// Go 与 Wasm 只能通过数字（整数/浮点）通信。
	// 为了传递字符串，必须先在 Wasm 内存中 malloc 空间，将字节写进去，最后传递内存地址（指针）。
	pPtr, pLen, err := c.copyStringToWasm(ctx, prompt)
	if err != nil {
		return "", err
	}
	defer c.freeWasmPtr(ctx, pPtr, pLen) // 使用完后释放 Wasm 侧的输入缓冲区。

	// B. 执行 FFI 调用：
	results, err := c.checkInput.Call(ctx, pPtr)
	if err != nil {
		return "", err
	}
	
	// C. 解析返回值：
	// 返回值是一个指针地址 resPtr，指向 Wasm 内存中已被 Rust 处理完的脱敏结果。
	resPtr := results[0]
	defer c.freeString.Call(ctx, resPtr) // 极其重要：释放 Rust 在堆上分配的结果字符串，防止内存泄漏。

	// D. 从 Wasm 内存地址读取数据回 Go。
	return c.getString(resPtr)
}

// getString 从指定的 Wasm 内存偏移地址读取 C 风格字符串。
func (c *WasmNitroClient) getString(offset uint64) (string, error) {
	mem := c.module.Memory()
	off32 := uint32(offset)
	
	// 计算安全读取上界。
	if off32 >= mem.Size() {
		return "", fmt.Errorf("Wasm 内存越界: %d", offset)
	}

	maxLen := mem.Size() - off32
	if maxLen > 10*1024*1024 { // 限制单次读取不能超过 10MB，防止异常崩溃。
		maxLen = 10 * 1024 * 1024
	}

	// 读取内存。
	bytes, ok := mem.Read(off32, maxLen)
	if !ok {
		return "", fmt.Errorf("解析 Wasm 线性内存失败")
	}

	// 寻找空终止符 \0。
	for i, v := range bytes {
		if v == 0 {
			return string(bytes[:i]), nil
		}
	}
	return string(bytes), nil
}

// copyStringToWasm 是将 Go 字符串“投影”到 Wasm 世界的辅助方法。
func (c *WasmNitroClient) copyStringToWasm(ctx context.Context, s string) (uint64, uint32, error) {
	size := uint64(len(s) + 1) // 包含结束符 \0。
	
	// 1. 在 Wasm 内存中开辟空间。
	results, err := c.malloc.Call(ctx, size)
	if err != nil {
		return 0, 0, err
	}
	ptr := results[0]
	
	// 2. 将数据从 Go 内存复制到 Wasm 线性内存。
	if !c.module.Memory().Write(uint32(ptr), append([]byte(s), 0)) {
		return 0, 0, fmt.Errorf("写入 Wasm 内存越界")
	}
	return ptr, uint32(size), nil
}

func (c *WasmNitroClient) Close() error {
	return c.runtime.Close(context.Background())
}

func (c *WasmNitroClient) SyncRules(ctx context.Context, rules string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	ptr, len, err := c.copyStringToWasm(ctx, rules)
	if err != nil {
		return err
	}
	defer c.freeWasmPtr(ctx, ptr, len)

	_, err = c.setRules.Call(ctx, ptr)
	return err
}

func (c *WasmNitroClient) CountTokens(ctx context.Context, model, text string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	mPtr, mLen, _ := c.copyStringToWasm(ctx, model)
	defer c.freeWasmPtr(ctx, mPtr, mLen)

	tPtr, tLen, _ := c.copyStringToWasm(ctx, text)
	defer c.freeWasmPtr(ctx, tPtr, tLen)

	results, err := c.countTokens.Call(ctx, mPtr, tPtr)
	if err != nil {
		return 0, err
	}
	return int(results[0]), nil
}

func (c *WasmNitroClient) freeWasmPtr(ctx context.Context, ptr uint64, size uint32) {
	c.freePtr.Call(ctx, ptr, uint64(size))
}
