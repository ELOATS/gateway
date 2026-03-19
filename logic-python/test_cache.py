import unittest
from main import AiLogicServicer
import gateway_pb2
import grpc

class TestCacheIsolation(unittest.TestCase):
    def setUp(self):
        self.servicer = AiLogicServicer()
        # 初始化一些缓存 (直接操作内部状态进行快速验证)
        from main import CACHE_STORE, FAISS_INDEX, EMBEDDING_MODEL
        import numpy as np
        
        # 清除旧数据
        CACHE_STORE.clear()
        FAISS_INDEX.reset()
        
        # 插入 GPT-3.5 的缓存
        prompt = "test cache"
        resp = "gpt3.5 answer"
        model = "gpt-3.5-turbo"
        self.servicer._add_to_cache(prompt, resp, model)

    def test_model_isolation(self):
        # 1. 使用相同模型查询，应当命中
        req_same = gateway_pb2.CacheRequest(prompt="test cache", model="gpt-3.5-turbo")
        res_same = self.servicer.GetCache(req_same, None)
        self.assertTrue(res_same.hit)
        self.assertEqual(res_same.response, "gpt3.5 answer")

        # 2. 使用不同模型查询，不应当命中 (物理隔离)
        req_diff = gateway_pb2.CacheRequest(prompt="test cache", model="gpt-4")
        res_diff = self.servicer.GetCache(req_diff, None)
        self.assertFalse(res_diff.hit)

        # 3. 使用 'any' 模型插入，任何模型都应命中
        self.servicer._add_to_cache("global", "shared answer", "any")
        
        req_gpt4 = gateway_pb2.CacheRequest(prompt="global", model="gpt-4")
        res_gpt4 = self.servicer.GetCache(req_gpt4, None)
        self.assertTrue(res_gpt4.hit)

if __name__ == '__main__':
    unittest.main()
