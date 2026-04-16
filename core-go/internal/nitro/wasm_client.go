package nitro

import (
	"context"
	"fmt"
	"os"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// wasmInstance 持有一个具体的 Wasm 模块实例及其导出函数句柄。
// 之所以拆出这个结构，是为了让实例池复用时不必反复查找导出函数。
type wasmInstance struct {
	module      api.Module
	malloc      api.Function
	freePtr     api.Function
	setRules    api.Function
	countTokens api.Function
	checkInput  api.Function
	freeString  api.Function
}

type WasmNitroClient struct {
	runtime wazero.Runtime
	code    wazero.CompiledModule
	pool    chan *wasmInstance
	rules   string
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

// syncRulesInst 把当前规则表写入指定实例，确保每个实例都持有一致的脱敏规则。
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

// copyStringToWasm 在 Wasm 侧分配内存并写入 Go 字符串。
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
