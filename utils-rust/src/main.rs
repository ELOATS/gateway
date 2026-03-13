//! Nitro Plane (Rust) - Performance-optimized gateway utilities.
//!
//! Enhanced with trace-id extraction for distributed observability.

use once_cell::sync::Lazy;
use regex::Regex;
use tiktoken_rs::{cl100k_base, p50k_base, r50k_base};
use tonic::{transport::Server, Request, Response, Status};

/// Global static regex for PII (Email) detection, compiled once.
static RE_EMAIL: Lazy<Regex> = Lazy::new(|| {
    Regex::new(r"[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}")
        .expect("Failed to compile PII email regex")
});

/// Generated gRPC code from proto/gateway.proto.
pub mod gateway {
    tonic::include_proto!("gateway");
}

use gateway::ai_logic_server::{AiLogic, AiLogicServer};
use gateway::{
    CacheRequest, CacheResponse, InputRequest, InputResponse, OutputRequest, OutputResponse,
    TokenRequest, TokenResponse,
};

/// MyAiLogic implements the gRPC service for high-performance AI utilities.
#[derive(Default)]
pub struct MyAiLogic {}

/// Helper to extract Request-ID from gRPC metadata.
/// Optimized to use shared references to avoid ownership moves.
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
            return Err(Status::invalid_argument("prompt cannot be empty"));
        }
        
        log::info!("[RID:{}] Nitro Plane: Masking PII", rid);

        // 使用 COW (Copy-On-Write) 优化替换，如果无替换则不产生分配
        let sanitized = RE_EMAIL
            .replace_all(&req.prompt, "[PII_EMAIL_MASKED]");

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

        let count = if model_name.contains("gpt-4") || model_name.contains("3.5-turbo") {
            let bpe = cl100k_base().map_err(|e| Status::internal(e.to_string()))?;
            bpe.encode_with_special_tokens(&req.text).len()
        } else if model_name.contains("davinci") {
            let bpe = p50k_base().map_err(|e| Status::internal(e.to_string()))?;
            bpe.encode_with_special_tokens(&req.text).len()
        } else {
            let bpe = r50k_base().map_err(|e| Status::internal(e.to_string()))?;
            bpe.encode_with_special_tokens(&req.text).len()
        };

        log::info!("[RID:{}] Nitro Plane: Token count: {}", rid, count);

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

    // Load shared environment variables.
    if let Ok(_) = dotenv::dotenv() {
        // Current directory
    } else if let Ok(_) = dotenv::from_path("../.env") {
        // Parent
    } else if let Ok(_) = dotenv::from_path("../../.env") {
        // Grandparent
    }

    let port = std::env::var("RUST_GRPC_PORT").or_else(|_| std::env::var("GRPC_PORT")).unwrap_or_else(|_| "50052".to_string());
    let addr = format!("[::1]:{}", port).parse()?;
    let ai_logic = MyAiLogic::default();

    println!("Nitro Utils Service (Rust) listening on {}", addr);

    Server::builder()
        .add_service(AiLogicServer::new(ai_logic))
        .serve(addr)
        .await?;

    Ok(())
}
