//! Nitro Rust 核心引擎
//! 
//! 设计意图：Nitro 是网关的安全与性能双重基石。
//! 它主要负责在极低延迟（<1ms）下执行敏感词过滤与精确的 Token 统计。
//! 为了实现零开销抽象，本核心既可以被编译为标准静态库供 gRPC 服务使用，
//! 也可以编译为 WebAssembly (WASM) 直接嵌入到 Go 运行时的内存空间中执行。

use once_cell::sync::Lazy;
use regex::Regex;
use serde::Deserialize;
use std::ffi::{CStr, CString};
use std::os::raw::c_char;
use std::sync::RwLock;
use tiktoken_rs::{cl100k_base, p50k_base, r50k_base, CoreBPE};

#[derive(Deserialize)]
struct SensitiveRule {
    pattern: String,
    replacement: String,
}

pub struct CompiledRule {
    pub pattern: Regex,
    pub replacement: String,
}

// 全局敏感词规则表。
// 设计决策：由 Go 管理面统一注入，通过 RwLock 保证多线程并发读取时的极致性能。
pub static SENSITIVE_RULES: Lazy<RwLock<Vec<CompiledRule>>> = Lazy::new(|| RwLock::new(vec![]));

// OpenAI 标准的 BPE（Byte Pair Encoding）分词编码器。
// 设计决策：这些编码器初始化成本极高（需加载海量映射表），通过 Lazy 模式确保在进程生命周期内只初始化一次。
pub static BPE_CL100K: Lazy<CoreBPE> =
    Lazy::new(|| cl100k_base().expect("failed to initialize cl100k_base"));
pub static BPE_P50K: Lazy<CoreBPE> =
    Lazy::new(|| p50k_base().expect("failed to initialize p50k_base"));
pub static BPE_R50K: Lazy<CoreBPE> =
    Lazy::new(|| r50k_base().expect("failed to initialize r50k_base"));

/// Nitro 内存分配器（FFI/WASM 专用）
/// 
/// 由于 WASM 与宿主环境（Go）属于完全隔离的内存空间，宿主必须通过该接口在 WASM 堆上分配空间来传递 Payload。
#[no_mangle]
pub extern "C" fn nitro_malloc(size: usize) -> *mut u8 {
    let layout = std::alloc::Layout::from_size_align(size, 1).unwrap();
    unsafe { std::alloc::alloc(layout) }
}

/// Nitro 内存回收器（FFI/WASM 专用）
/// 
/// 释放由 `nitro_malloc` 分配的内存。
/// 
/// # Safety
/// `ptr` 必须来自上面的 `nitro_malloc`，并且 `size` 必须与分配时保持一致。
#[no_mangle]
pub unsafe extern "C" fn free_ptr(ptr: *mut u8, size: usize) {
    if ptr.is_null() {
        return;
    }
    let layout = std::alloc::Layout::from_size_align(size, 1).unwrap();
    unsafe { std::alloc::dealloc(ptr, layout) }
}

/// # Safety
/// `rules_ptr` 必须是有效的、非空的、以 NUL 结尾的 UTF-8 字符串指针。
#[no_mangle]
pub unsafe extern "C" fn set_sensitive_rules_wasm(rules_ptr: *const c_char) {
    let rules_str = unsafe { CStr::from_ptr(rules_ptr).to_string_lossy() };
    if let Ok(new_rules) = serde_json::from_str::<Vec<SensitiveRule>>(&rules_str) {
        let compiled: Vec<CompiledRule> = new_rules
            .into_iter()
            .filter_map(|rule| {
                Regex::new(&rule.pattern).ok().map(|pattern| CompiledRule {
                    pattern,
                    replacement: rule.replacement,
                })
            })
            .collect();

        if let Ok(mut rules) = SENSITIVE_RULES.write() {
            *rules = compiled;
        }
    }
}

/// 计算特定文本在目标模型下的 Token 消耗（FFI/WASM 接口）。
/// 
/// # Safety
/// 为确保内存隔离安全，`model_ptr` 和 `text_ptr` 必须是有效的、非空的、以 NUL 结尾的 UTF-8 字符串指针。
#[no_mangle]
pub unsafe extern "C" fn count_tokens_wasm(
    model_ptr: *const c_char,
    text_ptr: *const c_char,
) -> i32 {
    let model = unsafe { CStr::from_ptr(model_ptr).to_string_lossy().to_lowercase() };
    let text = unsafe { CStr::from_ptr(text_ptr).to_string_lossy() };

    let bpe: &CoreBPE = match () {
        _ if model.contains("gpt-4") || model.contains("3.5-turbo") => &BPE_CL100K,
        _ if model.contains("davinci") => &BPE_P50K,
        _ => &BPE_R50K,
    };

    bpe.encode_with_special_tokens(&text).len() as i32
}

/// # Safety
/// `prompt_ptr` 必须是有效的、非空的、以 NUL 结尾的 UTF-8 字符串指针。
#[no_mangle]
pub unsafe extern "C" fn check_input_wasm(prompt_ptr: *const c_char) -> *mut c_char {
    let prompt = unsafe { CStr::from_ptr(prompt_ptr).to_string_lossy() };
    let mut sanitized = prompt.into_owned();

    if let Ok(rules) = SENSITIVE_RULES.read() {
        for rule in rules.iter() {
            sanitized = rule
                .pattern
                .replace_all(&sanitized, &rule.replacement)
                .to_string();
        }
    }

    let c_str = CString::new(sanitized).unwrap_or_else(|_| CString::new("error").unwrap());
    c_str.into_raw()
}

/// # Safety
/// `ptr` 必须来自 `check_input_wasm`，且只能释放一次。
#[no_mangle]
pub unsafe extern "C" fn free_string(ptr: *mut c_char) {
    if ptr.is_null() {
        return;
    }
    unsafe {
        let _ = CString::from_raw(ptr);
    }
}

#[cfg(feature = "service")]
pub mod service_impl {
    // 这里保留未来独立 Nitro 服务实现的入口。
    pub async fn run_server() -> Result<(), Box<dyn std::error::Error>> {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::ffi::CString;

    #[test]
    fn test_sensitive_rules_round_trip() {
        let rules = CString::new(r#"[{"pattern":"secret_[0-9]+","replacement":"[REDACTED]"}]"#)
            .expect("rules CString");
        unsafe { set_sensitive_rules_wasm(rules.as_ptr()) };

        let prompt = CString::new("token secret_42 should be masked").expect("prompt CString");
        let sanitized_ptr = unsafe { check_input_wasm(prompt.as_ptr()) };
        assert!(
            !sanitized_ptr.is_null(),
            "sanitized pointer should not be null"
        );

        let sanitized = unsafe { CStr::from_ptr(sanitized_ptr) }
            .to_string_lossy()
            .into_owned();
        assert_eq!(sanitized, "token [REDACTED] should be masked");

        unsafe { free_string(sanitized_ptr) };
    }
}
