//! Nitro Rust 核心能力，提供进程内脱敏和 Token 统计能力。

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

// 全局规则表由 Go/Python 侧注入，运行时只做只读匹配。
pub static SENSITIVE_RULES: Lazy<RwLock<Vec<CompiledRule>>> = Lazy::new(|| RwLock::new(vec![]));

// 三套 BPE 编码器在进程内懒加载，避免每次统计 Token 都重新初始化。
pub static BPE_CL100K: Lazy<CoreBPE> =
    Lazy::new(|| cl100k_base().expect("failed to initialize cl100k_base"));
pub static BPE_P50K: Lazy<CoreBPE> =
    Lazy::new(|| p50k_base().expect("failed to initialize p50k_base"));
pub static BPE_R50K: Lazy<CoreBPE> =
    Lazy::new(|| r50k_base().expect("failed to initialize r50k_base"));

#[no_mangle]
pub extern "C" fn malloc(size: usize) -> *mut u8 {
    let layout = std::alloc::Layout::from_size_align(size, 1).unwrap();
    unsafe { std::alloc::alloc(layout) }
}

/// # Safety
/// `ptr` 必须来自上面的 `malloc`，并且 `size` 必须与分配时保持一致。
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
            .map(|rule| CompiledRule {
                pattern: Regex::new(&rule.pattern).unwrap_or_else(|_| Regex::new("error").unwrap()),
                replacement: rule.replacement,
            })
            .collect();

        if let Ok(mut rules) = SENSITIVE_RULES.write() {
            *rules = compiled;
        }
    }
}

/// # Safety
/// `model_ptr` 和 `text_ptr` 必须是有效的、非空的、以 NUL 结尾的 UTF-8 字符串指针。
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
