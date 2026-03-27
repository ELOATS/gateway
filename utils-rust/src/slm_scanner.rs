use std::error::Error;

/// ScannerResult 表示从文本中识别出的一个敏感实体。
#[derive(Debug, Clone)]
pub struct ScannerResult {
    pub entity_type: String, // 例如 PHI、Financial、Name。
    pub start: usize,
    pub end: usize,
    pub text: String,
}

/// SlmScanner 定义基于小型语言模型的深度脱敏抽象。
///
/// 这个接口定位在“正则规则之后的补充层”：
/// 1. 未来可以接入本地 NER/分类模型，识别无固定模式的敏感片段。
/// 2. 当前网关主链路不依赖它，因此它更适合作为增强能力逐步演进。
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
