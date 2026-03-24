import unittest
from unittest.mock import patch

import gateway_pb2
from main import AiLogicServicer


class SearchHit:
    def __init__(self, score: float, payload: dict):
        self.score = score
        self.payload = payload


class FakeQdrantClient:
    def __init__(self):
        self.search_result = []
        self.upserts = []

    def search(self, **kwargs):
        self.last_search_kwargs = kwargs
        return self.search_result

    def upsert(self, **kwargs):
        self.upserts.append(kwargs)


class TestCacheIntegration(unittest.TestCase):
    def setUp(self):
        self.servicer = AiLogicServicer()
        self.fake_client = FakeQdrantClient()

    @patch("main.encode_texts", return_value=[[0.1, 0.2, 0.3]])
    @patch("main.get_qdrant_client")
    def test_cache_hit_returns_payload(self, mock_get_client, _mock_encode):
        mock_get_client.return_value = self.fake_client
        self.fake_client.search_result = [
            SearchHit(score=0.91, payload={"response": "cached answer"})
        ]

        req = gateway_pb2.CacheRequest(prompt="test cache", model="gpt-4")
        res = self.servicer.GetCache(req, None)

        self.assertTrue(res.hit)
        self.assertEqual(res.response, "cached answer")

    @patch("main.encode_texts", return_value=[[0.3, 0.2, 0.1]])
    @patch("main.get_qdrant_client")
    def test_add_to_cache_upserts_entry(self, mock_get_client, _mock_encode):
        mock_get_client.return_value = self.fake_client

        self.servicer._add_to_cache("hello", "world", "gpt-4")

        self.assertEqual(len(self.fake_client.upserts), 1)
        upsert = self.fake_client.upserts[0]
        self.assertEqual(upsert["collection_name"], "ai_gateway_cache")
        self.assertEqual(upsert["points"][0].payload["response"], "world")


if __name__ == "__main__":
    unittest.main()
