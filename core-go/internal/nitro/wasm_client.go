package nitro

import (
	"context"
	"fmt"
	"os"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// wasmInstance 持有一个具体的 Wasm 模块实例及其导出函数的底层句柄。
// 设计决策：通过拆分此结构，实现在实例池（Pool）复用时无需重新执行昂贵的导数查找（ExportedFunction）操作。
type wasmInstance struct {
	module      api.Module
	malloc      api.Function // Wasm 侧的内存分配函数
	freePtr     api.Function // Wasm 侧的内存回收函数
	setRules    api.Function // 敏感词规则注入函数
	countTokens api.Function // Token 统计逻辑函数
	checkInput  api.Function // 输入脱敏检测函数
	freeString  api.Function // 释放 Wasm 侧生成的 CString 函数
}

// WasmNitroClient 是 Nitro 引擎的嵌入式 WebAssembly 实现版。
// 
// 核心架构优势：
// 1. 极致低延迟：由于代码直接在 Go 进程的线性内存上运行，省去了网络 RPC 开销。
// 2. 隔离安全：即便 Rust 代码崩溃，也仅会影响 Wasm 虚拟机，不会导致 Go 宿主进程崩溃。
// 3. 高负载自解耦：通过实例池（Pool）管理 Wasm 实例，支持高并发下的计算密集型任务。
type WasmNitroClient struct {
	runtime wazero.Runtime        // Wazero 运行时环境
	code    wazero.CompiledModule // 预编译的 Wasm 二进制代码
	pool    chan *wasmInstance    // 实例复用池，减少重入时的初始化开销
	rules   string                // 初始化时注入的全局敏感词规则
}

// NewWasmNitroClient 加载 Wasm 模块并预热一个实例。
// 预热失败会直接返回错误，避免把“规则注入失败”延迟到首次请求时才暴露。
func NewWasmNitroClient(ctx context.Context, wasmPath string, sensitiveRules string) (*WasmNitroClient, error) {
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read wasm file: %w", err)
	}

	r := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	code, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("failed to compile wasm module: %w", err)
	}

	client := &WasmNitroClient{
		runtime: r,
		code:    code,
		pool:    make(chan *wasmInstance, 100),
		rules:   sensitiveRules,
	}

	inst, err := client.getInstance(ctx)
	if err != nil {
		_ = code.Close(ctx)
		_ = r.Close(ctx)
		return nil, err
	}
	client.putInstance(inst)

	return client, nil
}

func (c *WasmNitroClient) getInstance(ctx context.Context) (*wasmInstance, error) {
	select {
	case inst := <-c.pool:
		return inst, nil
	default:
		return newWasmInstance(ctx, c.runtime, c.code, c.rules)
	}
}

func (c *WasmNitroClient) putInstance(inst *wasmInstance) {
	select {
	case c.pool <- inst:
	default:
		_ = inst.module.Close(context.Background())
	}
}

// newWasmInstance 实例化一个新的 Wasm 模块，并在创建时完成规则同步。
func newWasmInstance(ctx context.Context, runtime wazero.Runtime, code wazero.CompiledModule, sensitiveRules string) (*wasmInstance, error) {
	mod, err := runtime.InstantiateModule(ctx, code, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("failed to instantiate wasm module: %w", err)
	}

	inst := &wasmInstance{
		module:      mod,
		malloc:      mod.ExportedFunction("nitro_malloc"),
		freePtr:     mod.ExportedFunction("free_ptr"),
		setRules:    mod.ExportedFunction("set_sensitive_rules_wasm"),
		countTokens: mod.ExportedFunction("count_tokens_wasm"),
		checkInput:  mod.ExportedFunction("check_input_wasm"),
		freeString:  mod.ExportedFunction("free_string"),
	}

	if sensitiveRules != "" {
		if err := syncRulesInst(ctx, inst, sensitiveRules); err != nil {
			_ = mod.Close(ctx)
			return nil, fmt.Errorf("failed to sync sensitive rules to wasm: %w", err)
		}
	}

	return inst, nil
}

// syncRulesInst 将全局敏感词规则同步至目标 Wasm 实例。
// 设计决策：每个 Wasm 实例拥有独立的线性内存。因此在创建新实例或更新规则时，
// 必须通过内存拷贝将规则字符串显式推送至 Wasm 内部的 Rust 虚拟机中。
func syncRulesInst(ctx context.Context, inst *wasmInstance, rules string) error {
	ptr, sz, err := copyStringToWasm(ctx, inst, rules)
	if err != nil {
		return err
	}
	defer freeWasmPtr(ctx, inst, ptr, sz)

	_, err = inst.setRules.Call(ctx, ptr)
	return err
}

func (c *WasmNitroClient) CheckInput(ctx context.Context, prompt string) (string, error) {
	inst, err := c.getInstance(ctx)
	if err != nil {
		return "", err
	}
	defer c.putInstance(inst)

	pPtr, pLen, err := copyStringToWasm(ctx, inst, prompt)
	if err != nil {
		return "", err
	}
	defer freeWasmPtr(ctx, inst, pPtr, pLen)

	results, err := inst.checkInput.Call(ctx, pPtr)
	if err != nil {
		return "", err
	}

	resPtr := results[0]
	defer inst.freeString.Call(ctx, resPtr)

	return getString(inst, resPtr)
}

// getString 从 Wasm 线性内存读取以 NUL 结尾的字符串，并做基本边界保护。
func getString(inst *wasmInstance, offset uint64) (string, error) {
	mem := inst.module.Memory()
	off32 := uint32(offset)

	if off32 >= mem.Size() {
		return "", fmt.Errorf("wasm memory out of bounds: %d", offset)
	}

	maxLen := mem.Size() - off32
	if maxLen > 10*1024*1024 {
		maxLen = 10 * 1024 * 1024
	}

	bytes, ok := mem.Read(off32, maxLen)
	if !ok {
		return "", fmt.Errorf("failed to read wasm linear memory")
	}

	for i, v := range bytes {
		if v == 0 {
			return string(bytes[:i]), nil
		}
	}
	return string(bytes), nil
}

// copyStringToWasm 是 Go 与 Wasm 跨语境通信的关键纽带。
//
// 逻辑流程：
// 1. 调用 Wasm 侧的 malloc 在其线性内存堆中申请一段空间。
// 2. 获取 Wasm 内存视图，直接将 Go 的字节切片（带 NUL 结束符）写入该地址。
// 3. 返回 Wasm 内部地址指针，供 Rust 代码作为 CString 处理。
func copyStringToWasm(ctx context.Context, inst *wasmInstance, s string) (uint64, uint32, error) {
	size := uint64(len(s) + 1)

	results, err := inst.malloc.Call(ctx, size)
	if err != nil {
		return 0, 0, err
	}
	ptr := results[0]

	if !inst.module.Memory().Write(uint32(ptr), append([]byte(s), 0)) {
		return 0, 0, fmt.Errorf("write exceeded wasm memory bounds")
	}
	return ptr, uint32(size), nil
}

// freeWasmPtr 释放先前分配给字符串拷贝的 Wasm 内存。
func freeWasmPtr(ctx context.Context, inst *wasmInstance, ptr uint64, size uint32) {
	inst.freePtr.Call(ctx, ptr, uint64(size))
}

func (c *WasmNitroClient) CountTokens(ctx context.Context, model, text string) (int, error) {
	inst, err := c.getInstance(ctx)
	if err != nil {
		return 0, err
	}
	defer c.putInstance(inst)

	mPtr, mLen, err := copyStringToWasm(ctx, inst, model)
	if err != nil {
		return 0, err
	}
	defer freeWasmPtr(ctx, inst, mPtr, mLen)

	tPtr, tLen, err := copyStringToWasm(ctx, inst, text)
	if err != nil {
		return 0, err
	}
	defer freeWasmPtr(ctx, inst, tPtr, tLen)

	results, err := inst.countTokens.Call(ctx, mPtr, tPtr)
	if err != nil {
		return 0, err
	}
	return int(results[0]), nil
}

// Close 关闭底层 wazero runtime，释放所有实例相关资源。
func (c *WasmNitroClient) Close() error {
	return c.runtime.Close(context.Background())
}
