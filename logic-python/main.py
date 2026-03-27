from __future__ import annotations

import concurrent.futures
import logging
import os
import uuid
from typing import Any, Optional

import grpc
from dotenv import load_dotenv
from qdrant_client import QdrantClient
from qdrant_client.models import Distance, FieldCondition, Filter, MatchValue, PointStruct, VectorParams

import gateway_pb2
import gateway_pb2_grpc
from prompt_injection import PromptInjectionDetector

load_dotenv(dotenv_path=".env")
load_dotenv(dotenv_path="../.env")

logging.basicConfig(
    level=logging.INFO,
    format='{"time":"%(asctime)s", "level":"%(levelname)s", "message":"%(message)s"}',
)
logger = logging.getLogger(__name__)

QDRANT_HOST = os.getenv("QDRANT_HOST", "localhost")
QDRANT_PORT = int(os.getenv("QDRANT_PORT", "6333"))
QDRANT_COLLECTION = os.getenv("QDRANT_COLLECTION", "ai_gateway_cache")
EMBEDDING_MODEL_NAME = os.getenv("EMBEDDING_MODEL_NAME", "all-MiniLM-L6-v2")
VECTOR_DIMENSION = 384

_embedding_model: Optional[Any] = None
_qdrant_client: Optional[QdrantClient] = None
_prompt_detector: Optional[PromptInjectionDetector] = None


def get_embedding_model() -> Any:
    """按需加载 embedding 模型，避免服务启动时就拉起重依赖。"""
    global _embedding_model
    if _embedding_model is None:
        from sentence_transformers import SentenceTransformer

        logger.info("loading embedding model: %s", EMBEDDING_MODEL_NAME)
        _embedding_model = SentenceTransformer(EMBEDDING_MODEL_NAME)
    return _embedding_model


def encode_texts(texts: list[str]) -> list[list[float]]:
    """把文本批量编码成向量，供缓存和轻量语义检测复用。"""
    vectors = get_embedding_model().encode(texts)
    return [list(vector) for vector in vectors]


def get_qdrant_client() -> QdrantClient:
    """延迟初始化 Qdrant 客户端，使缓存能力成为可选增强而非启动阻塞项。"""
    global _qdrant_client
    if _qdrant_client is None:
        _qdrant_client = QdrantClient(host=QDRANT_HOST, port=QDRANT_PORT)
    return _qdrant_client


def get_prompt_detector() -> PromptInjectionDetector:
    """复用单例检测器，避免每次请求都重建规则和原型向量。"""
    global _prompt_detector
    if _prompt_detector is None:
        _prompt_detector = PromptInjectionDetector(embed_fn=encode_texts)
    return _prompt_detector


def ensure_collection() -> None:
    """确保语义缓存集合存在；失败只记日志，不阻断主服务启动。"""
    try:
        client = get_qdrant_client()
        collections = client.get_collections().collections
        exists = any(collection.name == QDRANT_COLLECTION for collection in collections)
        if not exists:
            logger.info("creating qdrant collection: %s", QDRANT_COLLECTION)
            client.create_collection(
                collection_name=QDRANT_COLLECTION,
                vectors_config=VectorParams(size=VECTOR_DIMENSION, distance=Distance.COSINE),
            )
    except Exception as exc:
        logger.error("failed to connect to qdrant: %s", exc)


def get_request_id(context: grpc.ServicerContext) -> str:
    """尽量从 gRPC metadata 中提取请求 ID，便于跨语言链路排查。"""
    if context is None:
        return "unknown"
    metadata = dict(context.invocation_metadata())
    return metadata.get("x-request-id") or metadata.get("request-id") or "unknown"


class AiLogicServicer(gateway_pb2_grpc.AiLogicServicer):
    """Python 智能增强服务。

    这里承载的能力默认都应当是“可降级”的：即使不可用，也不能破坏 Go 主链路的基础正确性。
    """

    def CheckInput(
        self, request: gateway_pb2.InputRequest, context: grpc.ServicerContext
    ) -> gateway_pb2.InputResponse:
        """执行轻量输入护栏。

        这里保留基础规则和可选语义检测；真正的关键护栏仍由 Go/Rust 主链路兜底。
        """
        rid = get_request_id(context)
        logger.info("[RID:%s] running input guardrails", rid)

        prompt = request.prompt
        sanitized = prompt
        if "admin@company.com" in prompt:
            sanitized = sanitized.replace("admin@company.com", "[EMAIL_HIDDEN]")

        detection = get_prompt_detector().inspect(prompt)
        if not detection.safe:
            logger.warning("[RID:%s] prompt injection blocked: %s", rid, detection.reason)

        return gateway_pb2.InputResponse(
            safe=detection.safe,
            sanitized_prompt=sanitized if detection.safe else "",
            reason=detection.reason,
        )

    def CheckOutput(
        self, request: gateway_pb2.OutputRequest, context: grpc.ServicerContext
    ) -> gateway_pb2.OutputResponse:
        """执行轻量输出护栏，用于补充非关键风险信号。"""
        rid = get_request_id(context)
        logger.info("[RID:%s] running output guardrails", rid)

        text = request.response_text
        is_safe = "contradictory" not in text.lower()
        sanitized_text = text if is_safe else "[content blocked: contradictory output detected]"
        return gateway_pb2.OutputResponse(safe=is_safe, sanitized_text=sanitized_text)

    def GetCache(
        self, request: gateway_pb2.CacheRequest, context: grpc.ServicerContext
    ) -> gateway_pb2.CacheResponse:
        """按模型维度查询语义缓存。

        缓存命中属于性能增强，不应改变请求是否被允许执行的主语义。
        """
        rid = get_request_id(context)
        prompt = request.prompt
        target_model = request.model

        if not prompt:
            return gateway_pb2.CacheResponse(hit=False)

        query_vector = encode_texts([prompt])[0]

        try:
            search_result = get_qdrant_client().search(
                collection_name=QDRANT_COLLECTION,
                query_vector=query_vector,
                query_filter=Filter(
                    must=[
                        FieldCondition(
                            key="model",
                            match=MatchValue(value=target_model),
                        )
                    ]
                ),
                limit=1,
                score_threshold=0.85,
            )

            if search_result:
                best_hit = search_result[0]
                logger.info("[RID:%s] qdrant cache hit (score=%.4f)", rid, best_hit.score)
                return gateway_pb2.CacheResponse(
                    hit=True,
                    response=best_hit.payload.get("response", ""),
                )
        except Exception as exc:
            logger.error("[RID:%s] qdrant search failed: %s", rid, exc)

        if "what is 1+1" in prompt.lower():
            self._add_to_cache(prompt, "答案是 2。", target_model)

        return gateway_pb2.CacheResponse(hit=False)

    def _add_to_cache(self, prompt: str, response: str, model: str) -> None:
        """把成功样本写回 Qdrant，供后续相似请求复用。"""
        vector = encode_texts([prompt])[0]
        point_id = str(uuid.uuid4())

        try:
            get_qdrant_client().upsert(
                collection_name=QDRANT_COLLECTION,
                points=[
                    PointStruct(
                        id=point_id,
                        vector=vector,
                        payload={
                            "prompt": prompt,
                            "response": response,
                            "model": model,
                        },
                    )
                ],
            )
            logger.info("cache entry stored in qdrant: %s", point_id)
        except Exception as exc:
            logger.error("failed to upsert qdrant cache entry: %s", exc)


def build_server_credentials():
    """根据环境变量决定是否为 Python gRPC 服务开启 TLS/mTLS。"""
    if os.getenv("PYTHON_GRPC_TLS_ENABLED", "false").lower() != "true":
        return None

    cert_file = os.getenv("PYTHON_GRPC_CERT_FILE", "")
    key_file = os.getenv("PYTHON_GRPC_KEY_FILE", "")
    if not cert_file or not key_file:
        raise ValueError("PYTHON_GRPC_TLS_ENABLED requires PYTHON_GRPC_CERT_FILE and PYTHON_GRPC_KEY_FILE")

    with open(cert_file, "rb") as cert_handle:
        cert_chain = cert_handle.read()
    with open(key_file, "rb") as key_handle:
        private_key = key_handle.read()

    client_ca_file = os.getenv("PYTHON_GRPC_CLIENT_CA_FILE", "")
    require_client_cert = os.getenv("PYTHON_GRPC_REQUIRE_CLIENT_CERT", "false").lower() == "true"
    root_certificates = None
    if client_ca_file:
        with open(client_ca_file, "rb") as ca_handle:
            root_certificates = ca_handle.read()

    return grpc.ssl_server_credentials(
        [(private_key, cert_chain)],
        root_certificates=root_certificates,
        require_client_auth=require_client_cert,
    )


def serve() -> None:
    """启动 Python 侧 gRPC 服务。"""
    ensure_collection()

    port = os.getenv("PYTHON_GRPC_PORT", "50051")
    server = grpc.server(concurrent.futures.ThreadPoolExecutor(max_workers=20))
    gateway_pb2_grpc.add_AiLogicServicer_to_server(AiLogicServicer(), server)

    credentials = build_server_credentials()
    if credentials is None:
        server.add_insecure_port(f"[::]:{port}")
        logger.info("python ai logic gRPC server listening insecurely on %s", port)
    else:
        server.add_secure_port(f"[::]:{port}", credentials)
        logger.info("python ai logic gRPC server listening with TLS on %s", port)

    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
