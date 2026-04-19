use std::error::Error;

/// ScannerResult 表示从文本中识别出的一个敏感实体。
#[derive(Debug, Clone)]
pub struct ScannerResult {
    pub entity_type: String, // 例如 PHI、Financial、Name。
    pub start: usize,
    pub end: usize,
    pub text: String,
}

/// SlmScanner 定义了基于小型语言模型（SLM）或命名实体识别（NER）的深度安全扫描抽象接口。
///
/// 设计意图：
/// 它定位于 Nitro 引擎的“增强层”，作为传统正则表达式脱敏的补充：
/// 1. 深度识别：利用 NLP 模型（如 BERT 或轻量级 Transformer）识别那些无法通过正则固化模式匹配的敏感信息（如上下文相关的地址、自定义实体）。
/// 2. 插件化架构：当前保留接口定义与 Mock 实现，确保网关主链路在不引入底层依赖的前提下具备“即插即用”的扩展能力。
pub trait SlmScanner: Send + Sync {
    /// scan 分析输入文本，返回识别到的敏感实体列表。
    fn scan(&self, text: &str) -> Result<Vec<ScannerResult>, Box<dyn Error>>;

    /// sanitize 根据 scan 结果生成脱敏后的安全文本。
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
                // 统一替换成标准实体标签，便于上层继续处理和审计。
                safe_text.push_str(&format!("[{}_MASKED]", entity.entity_type));
                last_end = entity.end;
            }
        }

        safe_text.push_str(&text[last_end..]);
        Ok(safe_text)
    }
}

/// MockSlmScanner 是当前阶段的占位实现。
/// 它保留了接口形状，便于未来无缝替换成真实模型推理。
pub struct MockSlmScanner;

impl SlmScanner for MockSlmScanner {
    fn scan(&self, _text: &str) -> Result<Vec<ScannerResult>, Box<dyn Error>> {
        // 预研阶段暂不加载真实模型，这里先返回空结果。
        Ok(vec![])
    }
}
