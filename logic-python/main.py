import concurrent.futures
import json
import logging
import os
import uuid
from typing import List, Tuple, Optional

import grpc
import numpy as np
from sentence_transformers import SentenceTransformer
from qdrant_client import QdrantClient
from qdrant_client.http import models
from qdrant_client.models import PointStruct, VectorParams, Distance, Filter, FieldCondition, MatchValue
from dotenv import load_dotenv

import gateway_pb2
import gateway_pb2_grpc

# 加载环境变量
load_dotenv(dotenv_path=".env")
load_dotenv(dotenv_path="../.env")

# 日志配置
logging.basicConfig(
    level=logging.INFO,
    format='{"time":"%(asctime)s", "level":"%(levelname)s", "message":"%(message)s"}'
)
logger = logging.getLogger(__name__)

# --- Qdrant 配置 ---
QDRANT_HOST = os.getenv("QDRANT_HOST", "localhost")
QDRANT_PORT = int(os.getenv("QDRANT_PORT", "6333"))
QDRANT_COLLECTION = os.getenv("QDRANT_COLLECTION", "ai_gateway_cache")

# 初始化 Embedding 模型
logger.info("正在加载 Embedding 模型 (all-MiniLM-L6-v2)...")
EMBEDDING_MODEL = SentenceTransformer('all-MiniLM-L6-v2')
VECTOR_DIMENSION = 384

# 初始化 Qdrant 客户端 (云原生，无状态)
qdrant_client = QdrantClient(host=QDRANT_HOST, port=QDRANT_PORT)

def ensure_collection():
    """确保 Qdrant 集合存在。"""
    try:
        collections = qdrant_client.get_collections().collections
        exists = any(c.name == QDRANT_COLLECTION for c in collections)
        if not exists:
            logger.info("创建 Qdrant 集合: %s", QDRANT_COLLECTION)
            qdrant_client.create_collection(
                collection_name=QDRANT_COLLECTION,
                vectors_config=VectorParams(size=VECTOR_DIMENSION, distance=Distance.COSINE),
            )
    except Exception as e:
        logger.error("连接 Qdrant 失败 (请确保 Qdrant 服务已启动): %s", e)

def get_request_id(context: grpc.ServicerContext) -> str:
    metadata = dict(context.invocation_metadata())
	# 兼容不同的 Header 键名
    return metadata.get('x-request-id') or metadata.get('request-id') or 'unknown'

class AiLogicServicer(gateway_pb2_grpc.AiLogicServicer):
    """分布式智能审计服务。"""

    def CheckInput(self, request: gateway_pb2.InputRequest, 
                   context: grpc.ServicerContext) -> gateway_pb2.InputResponse:
        rid = get_request_id(context)
        logger.info("[RID:%s] 正在执行分布式输入审计", rid)
        
        prompt = request.prompt
        sanitized = prompt
        
        # 模拟敏感词逻辑
        if "admin@company.com" in prompt:
            sanitized = sanitized.replace("admin@company.com", "[EMAIL_HIDDEN]")

        is_safe = True
        block_reason = ""
        if "ignore all previous instructions" in prompt.lower():
            is_safe = False
            block_reason = "安全拦截: 检测到潜在的 Prompt Injection 攻击。"

        return gateway_pb2.InputResponse(
            safe=is_safe,
            sanitized_prompt=sanitized,
            reason=block_reason
        )

    def CheckOutput(self, request: gateway_pb2.OutputRequest, 
                    context: grpc.ServicerContext) -> gateway_pb2.OutputResponse:
        rid = get_request_id(context)
        logger.info("[RID:%s] 正在执行分布式输出审计", rid)
        
        text = request.response_text
        is_safe = "contradictory" not in text.lower()
        sanitized_text = text if is_safe else "[内容拦截: 检测到逻辑矛盾]"

        return gateway_pb2.OutputResponse(safe=is_safe, sanitized_text=sanitized_text)

    def GetCache(self, request: gateway_pb2.CacheRequest, 
                 context: grpc.ServicerContext) -> gateway_pb2.CacheResponse:
        """分布式分布式语义搜索。"""
        rid = get_request_id(context)
        prompt = request.prompt
        target_model = request.model
        
        if not prompt: return gateway_pb2.CacheResponse(hit=False)

        # 生成向量
        query_vector = EMBEDDING_MODEL.encode([prompt])[0].tolist()
        
        try:
            # 执行多维度过滤搜索
            search_result = qdrant_client.search(
                collection_name=QDRANT_COLLECTION,
                query_vector=query_vector,
                query_filter=Filter(
                    must=[
                        FieldCondition(
                            key="model",
                            match=MatchValue(value=target_model)
                        )
                    ]
                ),
                limit=1,
                score_threshold=0.85 # 余弦相似度阈值
            )

            if search_result:
                best_hit = search_result[0]
                logger.info("[RID:%s] Qdrant 语义缓存命中 (得分: %.4f)", rid, best_hit.score)
                return gateway_pb2.CacheResponse(
                    hit=True, 
                    response=best_hit.payload.get("response", "")
                )
        except Exception as e:
            logger.error("[RID:%s] Qdrant 检索失败: %s", rid, e)

        # 处理模拟规则（可选）
        if "what is 1+1" in prompt.lower():
            self._add_to_cache(prompt, "答案是 2。", target_model)

        return gateway_pb2.CacheResponse(hit=False)

    def _add_to_cache(self, prompt: str, response: str, model: str):
        """将新条目存入 Qdrant。"""
        vector = EMBEDDING_MODEL.encode([prompt])[0].tolist()
        point_id = str(uuid.uuid4())
        
        try:
            qdrant_client.upsert(
                collection_name=QDRANT_COLLECTION,
                points=[
                    PointStruct(
                        id=point_id,
                        vector=vector,
                        payload={
                            "prompt": prompt,
                            "response": response,
                            "model": model
                        }
                    )
                ]
            )
            logger.info("已同步至 Qdrant 共享缓存: %s", point_id)
        except Exception as e:
            logger.error("写入 Qdrant 失败: %s", e)

def serve():
    """启动分布式审计服务。"""
    ensure_collection()
    
    port = os.getenv("PYTHON_GRPC_PORT", "50051")
    server = grpc.server(concurrent.futures.ThreadPoolExecutor(max_workers=20))
    gateway_pb2_grpc.add_AiLogicServicer_to_server(AiLogicServicer(), server)
    server.add_insecure_port(f'[::]:{port}')
    logger.info("AI 智能审计服务 (Python/Qdrant) 已启动，监听端口: %s", port)
    server.start()
    server.wait_for_termination()

if __name__ == '__main__':
    serve()
