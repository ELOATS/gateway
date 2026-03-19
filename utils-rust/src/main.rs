//! Nitro 平面 (Rust) - 高性能网关工具组件。
//!
//! 针对分布式可观测性增强了追踪 ID (Trace-ID) 提取逻辑。

use arc_swap::ArcSwap;
use notify::{Config, Event, RecommendedWatcher, RecursiveMode, Watcher};
use once_cell::sync::Lazy;
use regex::Regex;
use std::path::Path;
use std::sync::Arc;
use tiktoken_rs::{cl100k_base, p50k_base, r50k_base};
use tonic::{transport::Server, Request, Response, Status};

/// 从 proto/gateway.proto 生成的 gRPC 代码。
pub mod gateway {
    tonic::include_proto!("gateway");
}

use gateway::ai_logic_server::{AiLogic, AiLogicServer};
use gateway::{
    CacheRequest, CacheResponse, InputRequest, InputResponse, OutputRequest, OutputResponse,
    TokenRequest, TokenResponse,
};
use tiktoken_rs::CoreBPE;

/// 配置文件路径
const SENSITIVE_CONFIG_PATH: &str = "configs/sensitive.txt";

/// 全局动态敏感词库，使用 ArcSwap 实现高性能并发读与原子级更新。
static SENSITIVE_RULES: Lazy<ArcSwap<Vec<Regex>>> = Lazy::new(|| {
    let rules = load_sensitive_rules(SENSITIVE_CONFIG_PATH);
    ArcSwap::from_pointee(rules)
});

/// 全局静态分词器，避免重复初始化开销。
static BPE_CL100K: Lazy<CoreBPE> = Lazy::new(|| cl100k_base().expect("无法初始化 cl100k_base"));
static BPE_P50K: Lazy<CoreBPE> = Lazy::new(|| p50k_base().expect("无法初始化 p50k_base"));
static BPE_R50K: Lazy<CoreBPE> = Lazy::new(|| r50k_base().expect("无法初始化 r50k_base"));

/// 从文件中加载正则表达式规则。
fn load_sensitive_rules(path: &str) -> Vec<Regex> {
    let mut rules = Vec::new();
    if let Ok(content) = std::fs::read_to_string(path) {
        for line in content.lines() {
            let line = line.trim();
            if line.is_empty() || line.starts_with('#') {
                continue;
            }
            if let Ok(re) = Regex::new(line) {
                rules.push(re);
            } else {
                eprintln!("警告：无效的正则语义：{}", line);
            }
        }
    }
    if rules.is_empty() {
        // 后备默认规则
        rules.push(Regex::new(r"[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}").unwrap());
    }
    rules
}

/// MyAiLogic 实现了高性能 AI 工具集的 gRPC 服务接口。
#[derive(Default)]
pub struct MyAiLogic {}

/// 从 gRPC 元数据中提取请求 ID (Request-ID)。
fn extract_rid<T>(req: &Request<T>) -> String {
    req.metadata()
        .get("x-request-id")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("unknown")
        .to_string()
}

#[tonic::async_trait]
impl AiLogic for MyAiLogic {
    async fn check_input(
        &self,
        request: Request<InputRequest>,
    ) -> Result<Response<InputResponse>, Status> {
        let rid = extract_rid(&request);
        let req = request.into_inner();
        
        if req.prompt.is_empty() {
            return Err(Status::invalid_argument("提示词不能为空"));
        }
        
        log::info!("[RID:{}] Nitro 平面：正在执行动态规则脱敏", rid);

        let mut sanitized = req.prompt.clone();
        
        // 加载当前内存中的动态规则列表
        let current_rules = SENSITIVE_RULES.load();
        for re in current_rules.iter() {
            sanitized = re.replace_all(&sanitized, "[MASKED]").to_string();
        }

        Ok(Response::new(InputResponse {
            safe: true,
            sanitized_prompt: sanitized,
            reason: String::new(),
        }))
    }

    async fn count_tokens(
        &self,
        request: Request<TokenRequest>,
    ) -> Result<Response<TokenResponse>, Status> {
        let rid = extract_rid(&request);
        let req = request.into_inner();
        let model_name = req.model.to_lowercase();

        let bpe: &CoreBPE = match () {
            _ if model_name.contains("gpt-4") || model_name.contains("3.5-turbo") => &BPE_CL100K,
            _ if model_name.contains("davinci") => &BPE_P50K,
            _ => &BPE_R50K,
        };

        let count = bpe.encode_with_special_tokens(&req.text).len();
        log::info!("[RID:{}] Nitro 平面：Token 统计结果: {}", rid, count);

        Ok(Response::new(TokenResponse {
            count: count as i32,
        }))
    }

    async fn check_output(
        &self,
        _request: Request<OutputRequest>,
    ) -> Result<Response<OutputResponse>, Status> {
        Ok(Response::new(OutputResponse {
            safe: true,
            sanitized_text: String::new(),
        }))
    }

    async fn get_cache(
        &self,
        _request: Request<CacheRequest>,
    ) -> Result<Response<CacheResponse>, Status> {
        Ok(Response::new(CacheResponse {
            hit: false,
            response: String::new(),
        }))
    }
}

/// 启动背景线程监控配置文件变动。
fn watch_config() {
    tokio::spawn(async move {
        let (tx, mut rx) = tokio::sync::mpsc::channel(1);
        let mut watcher = RecommendedWatcher::new(
            move |res| {
                let _ = tx.blocking_send(res);
            },
            Config::default(),
        ).expect("无法初始化文件监控器");

        watcher.watch(Path::new("configs"), RecursiveMode::NonRecursive).expect("监视路径失败");

        println!("正在监控 {} 以进行热重载...", SENSITIVE_CONFIG_PATH);

        while let Some(res) = rx.recv().await {
            match res {
                Ok(event) => {
                    if event.kind.is_modify() || event.kind.is_create() {
                        if event.paths.iter().any(|p| p.to_string_lossy().contains("sensitive.txt")) {
                            println!("检测到规则文件变动，正在重载...");
                            let new_rules = load_sensitive_rules(SENSITIVE_CONFIG_PATH);
                            SENSITIVE_RULES.store(Arc::new(new_rules));
                            println!("敏感词库重载成功。");
                        }
                    }
                }
                Err(e) => eprintln!("监控异常: {:?}", e),
            }
        }
    });
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    env_logger::init_from_env(env_logger::Env::default().default_filter_or("info"));

    if let Ok(_) = dotenv::dotenv() {
    } else if let Ok(_) = dotenv::from_path("../.env") {
    } else if let Ok(_) = dotenv::from_path("../../.env") {
    }

    // 预加载规则。
    Lazy::force(&SENSITIVE_RULES);
    
    // 开启热重载监听。
    watch_config();

    let port = std::env::var("RUST_GRPC_PORT").or_else(|_| std::env::var("GRPC_PORT")).unwrap_or_else(|_| "50052".to_string());
    let addr = format!("[::1]:{}", port).parse()?;
    let ai_logic = MyAiLogic::default();

    println!("Nitro 加速服务 (Rust) 已启动，监听地址: {}", addr);

    Server::builder()
        .add_service(AiLogicServer::new(ai_logic))
        .serve(addr)
        .await?;

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_email_masking() {
        // 创建临时测试文件
        let test_config = "test_sensitive.txt";
        std::fs::write(test_config, "[a-z]+@test\\.com\n").unwrap();
        
        let rules = load_sensitive_rules(test_config);
        assert!(!rules.is_empty());
        
        let prompt = "hello boss@test.com";
        let mut sanitized = prompt.to_string();
        for re in rules.iter() {
            sanitized = re.replace_all(&sanitized, "[MASKED]").to_string();
        }
        
        assert_eq!(sanitized, "hello [MASKED]");
        let _ = std::fs::remove_file(test_config);
    }

    #[test]
    fn test_empty_config_fallback() {
        let rules = load_sensitive_rules("non_existent.txt");
        assert!(!rules.is_empty());
        // 默认规则应包含 Email 正则
        let res = rules[0].is_match("user@example.com");
        assert!(res);
    }
}
