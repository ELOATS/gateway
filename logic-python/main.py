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
    """从磁盘加载持久化缓存（如果存在）。"""
    global FAISS_INDEX, CACHE_STORE
    
    if not os.path.exists(CACHE_DIR):
        os.makedirs(CACHE_DIR)
        return

    if not (os.path.exists(INDEX_PATH) and os.path.exists(STORE_PATH)):
        return

    with cache_lock:
        try:
            FAISS_INDEX = faiss.read_index(INDEX_PATH)
            with open(STORE_PATH, 'r', encoding='utf-8') as f:
                CACHE_STORE = [tuple(item) for item in json.load(f)]
            logger.info("已从磁盘加载 %d 条缓存记录", len(CACHE_STORE))
        except Exception as e:
            logger.error("加载缓存失败: %s", e)


def save_cache():
    """将缓存持久化到磁盘。"""
    with cache_lock:
        if not os.path.exists(CACHE_DIR):
            os.makedirs(CACHE_DIR)
        try:
            faiss.write_index(FAISS_INDEX, INDEX_PATH)
            with open(STORE_PATH, 'w', encoding='utf-8') as f:
                json.dump(CACHE_STORE, f, ensure_ascii=False, indent=2)
            logger.info("已将缓存保存到磁盘（共 %d 条）", len(CACHE_STORE))
        except Exception as e:
            logger.error("保存缓存失败: %s", e)


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
        """检查输入安全性并执行脱敏。"""
        rid = get_request_id(context)
        logger.info("[RID:%s] 正在执行输入安全审计", rid)
        
        prompt = request.prompt
        sanitized = prompt

        if "admin@company.com" in prompt:
            sanitized = sanitized.replace("admin@company.com", "[EMAIL_MASKED]")

        is_safe = True
        block_reason = ""
        if "ignore all previous instructions" in prompt.lower():
            is_safe = False
            block_reason = "安全拦截：检测到潜在的提示词注入攻击。"

        return gateway_pb2.InputResponse(
            safe=is_safe,
            sanitized_prompt=sanitized,
            reason=block_reason
        )

    def CheckOutput(self, request: gateway_pb2.OutputRequest, 
                    context: grpc.ServicerContext) -> gateway_pb2.OutputResponse:
        """检查响应内容的安全性（如幻觉检测）。"""
        rid = get_request_id(context)
        logger.info("[RID:%s] 正在执行输出内容审计", rid)
        
        text = request.response_text
        is_safe = True
        sanitized_text = text

        if "contradictory" in text.lower():
            is_safe = False
            sanitized_text = "[内容拦截：检测到潜在的逻辑自相矛盾/幻觉]"

        return gateway_pb2.OutputResponse(
            safe=is_safe,
            sanitized_text=sanitized_text
        )

    def GetCache(self, request: gateway_pb2.CacheRequest, 
                 context: grpc.ServicerContext) -> gateway_pb2.CacheResponse:
        """使用向量相似度执行语义缓存搜索。"""
        rid = get_request_id(context)
        logger.info("[RID:%s] 正在搜索语义缓存", rid)
        
        prompt = request.prompt
        if not prompt:
            return gateway_pb2.CacheResponse(hit=False)

        # 在锁外部生成 Embedding，避免阻塞其他请求
        embedding = EMBEDDING_MODEL.encode([prompt]).astype('float32')
        
        # 1. 在 FAISS 索引中搜索
        with cache_lock:
            if FAISS_INDEX.ntotal == 0:
                pass # 后续进入模拟逻辑
            else:
                distances, indices = FAISS_INDEX.search(embedding, 1)
                # L2 距离阈值：< 0.2 表示语义极度相似
                if distances[0][0] < 0.2:
                    match_idx = indices[0][0]
                    logger.info("[RID:%s] 语义缓存命中 (距离: %.4f)", rid, distances[0][0])
                    _, cached_response = CACHE_STORE[match_idx]
                    return gateway_pb2.CacheResponse(hit=True, response=cached_response)

        # 2. 模拟/测试逻辑
        self._handle_mock_cache(prompt, rid)

        logger.info("[RID:%s] 语义缓存未命中", rid)
        return gateway_pb2.CacheResponse(hit=False)

    def _handle_mock_cache(self, prompt: str, rid: str):
        """处理模拟/测试用的缓存项。"""
        if "what is 1+1" in prompt.lower():
            logger.info("[RID:%s] 正在为 '1+1' 构建模拟缓存响应", rid)
            self._add_to_cache(prompt, "答案是 2。")

    def _add_to_cache(self, prompt: str, response: str):
        """将新条目添加到语义索引并持久化到磁盘。"""
        embedding = EMBEDDING_MODEL.encode([prompt]).astype('float32')
        
        with cache_lock:
            FAISS_INDEX.add(embedding)
            CACHE_STORE.append((prompt, response))
        
        # 保存到磁盘。save_cache 内部会处理锁。
        save_cache()


def serve():
    """启动 gRPC 服务器。"""
    load_cache()
    # 从环境变量读取端口，默认为 50051
    port = os.getenv("PYTHON_GRPC_PORT", os.getenv("GRPC_PORT", "50051"))
    server = grpc.server(concurrent.futures.ThreadPoolExecutor(max_workers=10))
    gateway_pb2_grpc.add_AiLogicServicer_to_server(AiLogicServicer(), server)
    server.add_insecure_port(f'[::]:{port}')
    logger.info("AI 智能审计服务 (Python) 已启动，监听端口: %s", port)
    server.start()
    server.wait_for_termination()


if __name__ == '__main__':
    serve()
