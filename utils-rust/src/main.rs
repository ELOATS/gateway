//! Nitro 平面 (Rust) - 高性能网关工具组件。
//!
//! 针对分布式可观测性增强了追踪 ID (Trace-ID) 提取逻辑。

use once_cell::sync::Lazy;
use regex::Regex;
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

/// 全局静态正则表达式，用于检测并脱敏电子邮件（PII）。
static RE_EMAIL: Lazy<Regex> = Lazy::new(|| {
    Regex::new(r"[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}")
        .expect("无法编译 PII 电子邮件正则表达式")
});

/// 全局静态分词器，避免重复初始化开销。
static BPE_CL100K: Lazy<CoreBPE> = Lazy::new(|| cl100k_base().expect("无法初始化 cl100k_base"));
static BPE_P50K: Lazy<CoreBPE> = Lazy::new(|| p50k_base().expect("无法初始化 p50k_base"));
static BPE_R50K: Lazy<CoreBPE> = Lazy::new(|| r50k_base().expect("无法初始化 r50k_base"));

/// MyAiLogic 实现了高性能 AI 工具集的 gRPC 服务接口。
#[derive(Default)]
pub struct MyAiLogic {}

/// 从 gRPC 元数据中提取请求 ID (Request-ID)。
/// 优化为使用共享引用以避免所有权转移带来的开销。
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
        
        log::info!("[RID:{}] Nitro 平面：正在执行 PII 脱敏", rid);

        // 使用全局静态正则进行脱敏集。
        let sanitized = RE_EMAIL.replace_all(&req.prompt, "[PII_EMAIL_MASKED]");

        Ok(Response::new(InputResponse {
            safe: true,
            sanitized_prompt: sanitized.to_string(),
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

        // 使用全局预初始化的分词器。
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

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    env_logger::init_from_env(env_logger::Env::default().default_filter_or("info"));

    // 加载共享环境变量。
    if let Ok(_) = dotenv::dotenv() {
        // 当前目录
    } else if let Ok(_) = dotenv::from_path("../.env") {
        // 父目录
    } else if let Ok(_) = dotenv::from_path("../../.env") {
        // 祖父目录
    }

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
