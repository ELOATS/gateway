use std::error::Error;

/// ScannerResult 存储从上下文中提取到的敏感实体。
#[derive(Debug, Clone)]
pub struct ScannerResult {
    pub entity_type: String, // 如 "PHI" (医疗), "Financial" (金融), "Name" (姓名)
    pub start: usize,
    pub end: usize,
    pub text: String,
}

/// SlmScanner 定义了基于小型语言模型（SLM）进行深度脱敏的抽象层。
/// 
/// 未来规划：
/// 1. 通过 `tch-rs` 或 `ort` (ONNX Runtime) 加载本地部署的极小参数量 NER 模型（如 100M 参数的 BERT 变体）。
/// 2. 在正则引擎 (Regex) 之后作为补充方案运行，专门狩猎“无固定模式”的上下文隐私泄漏。
pub trait SlmScanner: Send + Sync {
    /// scan 分析给定的长文本，返回检测到的敏感实体列表。
    fn scan(&self, text: &str) -> Result<Vec<ScannerResult>, Box<dyn Error>>;

    /// sanitize 将检测到的实体通过特定的 Mask 或 Hash 算法替换，并返回安全文本。
    fn sanitize(&self, text: &str) -> Result<String, Box<dyn Error>> {
        let entities = self.scan(text)?;
        if entities.is_empty() {
            return Ok(text.to_string());
        }

        let mut safe_text = String::with_capacity(text.len());
        let mut last_end = 0;
        
        for entity in entities {
            if entity.start >= last_end {
                safe_text.push_str(&text[last_end..entity.start]);
                // 替换为标准的实体类型标识
                safe_text.push_str(&format!("[{}_MASKED]", entity.entity_type));
                last_end = entity.end;
            }
        }
        
        safe_text.push_str(&text[last_end..]);
        Ok(safe_text)
    }
}

/// MockSlmScanner 是一个预研阶段的空白实现，用于目前架构的占位与依赖注入。
pub struct MockSlmScanner;

impl SlmScanner for MockSlmScanner {
    fn scan(&self, _text: &str) -> Result<Vec<ScannerResult>, Box<dyn Error>> {
        // [预研阶段]: 尚未加载真实模型，目前直接返回空。
        // TBD: 初始化 ONNX Runtime Session 并在此处执行推断。
        Ok(vec![])
    }
}
