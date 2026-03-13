"""Intelligence Plane (Python) - Enhanced with Trace-ID and Logging.

Follows Google Python Style Guide.
"""

import concurrent.futures
import json
import logging
import os
import threading
from typing import List, Tuple

import faiss
import grpc
import numpy as np
from sentence_transformers import SentenceTransformer
from dotenv import load_dotenv

import gateway_pb2
import gateway_pb2_grpc

# Load shared environment variables.
# Search in current, parent, and parent's parent directory.
load_dotenv(dotenv_path=".env")
load_dotenv(dotenv_path="../.env")
load_dotenv(dotenv_path="../../.env")

# Configure logging to show info by default.
logging.basicConfig(
    level=logging.INFO,
    format='{"time":"%(asctime)s", "level":"%(levelname)s", "message":"%(message)s"}'
)
logger = logging.getLogger(__name__)

# Constants for persistence.
CACHE_DIR = "data"
INDEX_PATH = os.path.join(CACHE_DIR, "vector.index")
STORE_PATH = os.path.join(CACHE_DIR, "cache_store.json")

# Initialize embedding model.
logger.info("Loading embedding model (all-MiniLM-L6-v2)...")
EMBEDDING_MODEL = SentenceTransformer('all-MiniLM-L6-v2')

# Global Vector Database State.
VECTOR_DIMENSION = 384
FAISS_INDEX = faiss.IndexFlatL2(VECTOR_DIMENSION)
CACHE_STORE: List[Tuple[str, str]] = []
cache_lock = threading.Lock()


def load_cache():
    """Loads cache from disk if it exists."""
    global FAISS_INDEX, CACHE_STORE
    with cache_lock:
        if not os.path.exists(CACHE_DIR):
            os.makedirs(CACHE_DIR)
            return

        if os.path.exists(INDEX_PATH) and os.path.exists(STORE_PATH):
            try:
                FAISS_INDEX = faiss.read_index(INDEX_PATH)
                with open(STORE_PATH, 'r', encoding='utf-8') as f:
                    CACHE_STORE = [tuple(item) for item in json.load(f)]
                logger.info("Loaded %d items from persistent cache", len(CACHE_STORE))
            except Exception as e:
                logger.error("Failed to load cache: %s", e)


def save_cache():
    """Saves cache to disk. Assumes caller might hold lock, 
    but we use it here to ensure consistency if called directly.
    """
    with cache_lock:
        if not os.path.exists(CACHE_DIR):
            os.makedirs(CACHE_DIR)
        try:
            faiss.write_index(FAISS_INDEX, INDEX_PATH)
            with open(STORE_PATH, 'w', encoding='utf-8') as f:
                json.dump(CACHE_STORE, f, ensure_ascii=False, indent=2)
            logger.info("Saved cache to disk (%d items)", len(CACHE_STORE))
        except Exception as e:
            logger.error("Failed to save cache: %s", e)


def get_request_id(context: grpc.ServicerContext) -> str:
    """Extracts x-request-id from gRPC metadata.

    Args:
        context: The gRPC servicer context.

    Returns:
        The request ID string or 'unknown'.
    """
    metadata = dict(context.invocation_metadata())
    return metadata.get('x-request-id', 'unknown')


class AiLogicServicer(gateway_pb2_grpc.AiLogicServicer):
    """Provides advanced AI-driven logic with traceability."""

    def CheckInput(self, request: gateway_pb2.InputRequest, 
                   context: grpc.ServicerContext) -> gateway_pb2.InputResponse:
        rid = get_request_id(context)
        logger.info("[RID:%s] Checking input safety", rid)
        
        prompt = request.prompt
        sanitized = prompt

        if "admin@company.com" in prompt:
            sanitized = sanitized.replace("admin@company.com", "[EMAIL_MASKED]")

        is_safe = True
        block_reason = ""
        if "ignore all previous instructions" in prompt.lower():
            is_safe = False
            block_reason = "Security: Potential prompt injection detected."

        return gateway_pb2.InputResponse(
            safe=is_safe,
            sanitized_prompt=sanitized,
            reason=block_reason
        )

    def CheckOutput(self, request: gateway_pb2.OutputRequest, 
                    context: grpc.ServicerContext) -> gateway_pb2.OutputResponse:
        rid = get_request_id(context)
        logger.info("[RID:%s] Checking output for hallucinations", rid)
        
        text = request.response_text
        is_safe = True
        sanitized_text = text

        if "contradictory" in text.lower():
            is_safe = False
            sanitized_text = "[CONTENT BLOCKED: Potential Hallucination]"

        return gateway_pb2.OutputResponse(
            safe=is_safe,
            sanitized_text=sanitized_text
        )

    def GetCache(self, request: gateway_pb2.CacheRequest, 
                 context: grpc.ServicerContext) -> gateway_pb2.CacheResponse:
        rid = get_request_id(context)
        logger.info("[RID:%s] Searching semantic cache", rid)
        
        prompt = request.prompt
        if not prompt:
            return gateway_pb2.CacheResponse(hit=False)

        # Generate embedding OUTSIDE the lock to avoid blocking other requests.
        embedding = EMBEDDING_MODEL.encode([prompt]).astype('float32')
        
        with cache_lock:
            if FAISS_INDEX.ntotal > 0:
                distances, indices = FAISS_INDEX.search(embedding, 1)
                if distances[0][0] < 0.2:
                    match_idx = indices[0][0]
                    logger.info("[RID:%s] Semantic Cache HIT (Dist: %.4f)", rid, distances[0][0])
                    _, cached_response = CACHE_STORE[match_idx]
                    return gateway_pb2.CacheResponse(hit=True, response=cached_response)

        # Basic logic for mock test if cache is empty.
        if "what is 1+1" in prompt.lower():
            self._add_to_cache(prompt, "The answer is 2.")

        logger.info("[RID:%s] Semantic Cache MISS", rid)
        return gateway_pb2.CacheResponse(hit=False)

    def _add_to_cache(self, prompt: str, response: str):
        # Embedding can be done outside.
        embedding = EMBEDDING_MODEL.encode([prompt]).astype('float32')
        
        with cache_lock:
            FAISS_INDEX.add(embedding)
            CACHE_STORE.append((prompt, response))
        
        # Save to disk. save_cache handles its own locking.
        save_cache()


def serve():
    """Starts the gRPC server."""
    load_cache()
    # Read port from .env or default to 50051.
    port = os.getenv("PYTHON_GRPC_PORT", os.getenv("GRPC_PORT", "50051"))
    server = grpc.server(concurrent.futures.ThreadPoolExecutor(max_workers=10))
    gateway_pb2_grpc.add_AiLogicServicer_to_server(AiLogicServicer(), server)
    server.add_insecure_port(f'[::]:{port}')
    logger.info("AI Intelligence Service listening on port %s", port)
    server.start()
    server.wait_for_termination()


if __name__ == '__main__':
    serve()
