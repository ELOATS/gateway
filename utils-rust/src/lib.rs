//! Nitro 平面 (Rust) - Wasm Core (Unknown-Unknown)
//! 专注于跨语言、进程内的高性能脱敏与分词算子。

use once_cell::sync::Lazy;
use regex::Regex;
use serde::Deserialize;
use std::sync::{Arc, RwLock};
use tiktoken_rs::{cl100k_base, p50k_base, r50k_base, CoreBPE};
use std::ffi::{CStr, CString};
use std::os::raw::c_char;

// --- 核心数据结构 ---

/// SensitiveRule 定义了从 Host 端 (Go) 传入的单条脱敏规则。
#[derive(Deserialize)]
struct SensitiveRule {
    pattern: String,      // 正则表达式字符串
    replacement: String,   // 替换后的脱敏占位符 (例如 "[EMAIL]")
}

/// CompiledRule 是在 Rust 内部运行时的规则表示，包含了已编译的正则对象。
pub struct CompiledRule {
    pub pattern: Regex,
    pub replacement: String,
}

// --- 全局静态状态 (Lazy Initialization) ---

/// SENSITIVE_RULES 存储了当前的脱敏规则池。
/// 使用 RwLock 实现读写分离的并发访问。
pub static SENSITIVE_RULES: Lazy<RwLock<Vec<CompiledRule>>> = Lazy::new(|| {
    RwLock::new(vec![])
});

/// BPE 编码器单例。
/// Tiktoken 的初始化非常耗时，因此在 Wasm 加载时通过 Lazy 模式执行单次加载，后续请求即时响应。
pub static BPE_CL100K: Lazy<CoreBPE> = Lazy::new(|| cl100k_base().expect("无法初始化 cl100k_base"));
pub static BPE_P50K: Lazy<CoreBPE> = Lazy::new(|| p50k_base().expect("无法初始化 p50k_base"));
pub static BPE_R50K: Lazy<CoreBPE> = Lazy::new(|| r50k_base().expect("无法初始化 r50k_base"));

// --- FFI (Foreign Function Interface) 导出接口 ---
// 注意：以下所有 #[no_mangle] 函数均被 Go 侧通过 WebAssembly 线性内存地址直接调用。

/// malloc: 内存分配器供 Host 调用。
/// 
/// 【学习要点】: WebAssembly 拥有独立的线性内存。Go 侧无法直接访问 Rust 的堆空间。
/// 因此，Go 需要先调用 malloc 在 Wasm 内存中开辟一段空间，然后将参数数据（如 Prompt）写进去。
#[no_mangle]
pub extern "C" fn malloc(size: usize) -> *mut u8 {
    let layout = std::alloc::Layout::from_size_align(size, 1).unwrap();
    unsafe { std::alloc::alloc(layout) }
}

/// free_ptr: 内存释放器供 Host 调用。
/// 
/// 【学习要点】: 遵循“谁申请谁释放”原则。Go 侧在读取完数据后，必须调用此函数将内存所有权归还给 Rust 释放。
#[no_mangle]
pub extern "C" fn free_ptr(ptr: *mut u8, size: usize) {
    if ptr.is_null() { return; }
    let layout = std::alloc::Layout::from_size_align(size, 1).unwrap();
    unsafe { std::alloc::dealloc(ptr, layout) }
}

/// set_sensitive_rules_wasm: 解析 JSON 规则并热更新至核心引擎。
/// 
/// @param rules_ptr: 指向 Wasm 内存中 JSON 字符串的 C 风格指针 (\0 结尾)。
#[no_mangle]
pub extern "C" fn set_sensitive_rules_wasm(rules_ptr: *const c_char) {
    // 将指针转换为 Rust 字符串引用。
    let rules_str = unsafe { CStr::from_ptr(rules_ptr).to_string_lossy() };
    
    // 使用 serde 实现高性能 JSON 反序列化。
    if let Ok(new_rules) = serde_json::from_str::<Vec<SensitiveRule>>(&rules_str) {
        let compiled: Vec<CompiledRule> = new_rules.into_iter()
            .map(|r| CompiledRule {
                // 编译正则。如果正则语法错误，则降级为无效正则，确保 Wasm 模块不崩溃 (Crash-Safe)。
                pattern: Regex::new(&r.pattern).unwrap_or_else(|_| Regex::new("error").unwrap()),
                replacement: r.replacement,
            })
            .collect();
        // 加锁更新
        if let Ok(mut rules) = SENSITIVE_RULES.write() {
            *rules = compiled;
        }
    }
}

/// count_tokens_wasm: 进程内高性能分词。
/// 消除了 gRPC 通讯延迟，使得单次 Token 统计从 ms 级降至 ns 级。
#[no_mangle]
pub extern "C" fn count_tokens_wasm(model_ptr: *const c_char, text_ptr: *const c_char) -> i32 {
    let model = unsafe { CStr::from_ptr(model_ptr).to_string_lossy().to_lowercase() };
    let text = unsafe { CStr::from_ptr(text_ptr).to_string_lossy() };
    
    let bpe: &CoreBPE = match () {
        _ if model.contains("gpt-4") || model.contains("3.5-turbo") => &BPE_CL100K,
        _ if model.contains("davinci") => &BPE_P50K,
        _ => &BPE_R50K,
    };
    bpe.encode_with_special_tokens(&text).len() as i32
}

/// check_input_wasm: 核心脱敏算计。
/// 
/// @return: 返回一个新的 C 风格指针，指向处理后的字符串。
/// 【关键点】: 返回的指针由 CString::into_raw 产生，它是处于 Rust 控制之外的。
/// Go 侧读取完后必须显式调用 free_string。
#[no_mangle]
pub extern "C" fn check_input_wasm(prompt_ptr: *const c_char) -> *mut c_char {
    let prompt = unsafe { CStr::from_ptr(prompt_ptr).to_string_lossy() };
    let mut sanitized = prompt.into_owned();
    
    if let Ok(rules) = SENSITIVE_RULES.read() {
        for rule in rules.iter() {
            // 使用正则执行批量替换。
            sanitized = rule.pattern.replace_all(&sanitized, &rule.replacement).to_string();
        }
    }
    
    // 将 Rust String 包装为 CString 并导出为原始指针。
    let c_str = CString::new(sanitized).unwrap_or_else(|_| CString::new("error").unwrap());
    c_str.into_raw()
}

/// free_string: 专用字符串解构器。
/// 用于释放由 CString::into_raw 分配的堆内存。
#[no_mangle]
pub extern "C" fn free_string(ptr: *mut c_char) {
    if ptr.is_null() { return; }
    // 重新接管指针的所有权，并在作用域结束时自动触发回收。
    unsafe { let _ = CString::from_raw(ptr); }
}

/// 兼容层：保留 gRPC 服务接口定义但不执行逻辑。
#[cfg(feature = "service")]
pub mod service_impl {
    pub async fn run_server() -> Result<(), Box<dyn std::error::Error>> { Ok(()) }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_bpe_initialization() {
        // 尝试初始化 BPE 编译器，观察是否在 CI 环境下触发 SIGSEGV
        println!("Initializing BPE_CL100K...");
        let len = BPE_CL100K.encode_with_special_tokens("test").len();
        assert!(len > 0);
        println!("BPE_CL100K initialized successfully.");
    }
}
