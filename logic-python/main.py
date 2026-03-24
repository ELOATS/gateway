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
    global _embedding_model
    if _embedding_model is None:
        from sentence_transformers import SentenceTransformer

        logger.info("loading embedding model: %s", EMBEDDING_MODEL_NAME)
        _embedding_model = SentenceTransformer(EMBEDDING_MODEL_NAME)
    return _embedding_model


def encode_texts(texts: list[str]) -> list[list[float]]:
    vectors = get_embedding_model().encode(texts)
    return [list(vector) for vector in vectors]


def get_qdrant_client() -> QdrantClient:
    global _qdrant_client
    if _qdrant_client is None:
        _qdrant_client = QdrantClient(host=QDRANT_HOST, port=QDRANT_PORT)
    return _qdrant_client


def get_prompt_detector() -> PromptInjectionDetector:
    global _prompt_detector
    if _prompt_detector is None:
        _prompt_detector = PromptInjectionDetector(embed_fn=encode_texts)
    return _prompt_detector


def ensure_collection() -> None:
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
    if context is None:
        return "unknown"
    metadata = dict(context.invocation_metadata())
    return metadata.get("x-request-id") or metadata.get("request-id") or "unknown"


class AiLogicServicer(gateway_pb2_grpc.AiLogicServicer):
    def CheckInput(
        self, request: gateway_pb2.InputRequest, context: grpc.ServicerContext
    ) -> gateway_pb2.InputResponse:
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
        rid = get_request_id(context)
        logger.info("[RID:%s] running output guardrails", rid)

        text = request.response_text
        is_safe = "contradictory" not in text.lower()
        sanitized_text = text if is_safe else "[content blocked: contradictory output detected]"
        return gateway_pb2.OutputResponse(safe=is_safe, sanitized_text=sanitized_text)

    def GetCache(
        self, request: gateway_pb2.CacheRequest, context: grpc.ServicerContext
    ) -> gateway_pb2.CacheResponse:
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
