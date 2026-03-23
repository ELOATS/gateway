# 分布式智能底座：Qdrant 向量检索实战指南

在 NITRO 2.0 中，我们将语义缓存层从单机版（Local Faiss）升级为了分布式版（Qdrant Cluster）。本指南将带您深入理解这一核心转变。

## 1. 为什么放弃 Faiss？

Faiss 是一款极其卓越的向量搜索库，但在构建**分布式、自动扩缩容**的网关时，它存在以下局限：
- **无状态性冲突**: Faiss 索引需要定期保存为本地 `.index` 文件。这就要求 Python 节点必须挂载持久卷（PVC），否则重启后缓存归零。
- **并发同步困难**: 多个 Python 实例无法实时共享彼此插入的新缓存，除非通过复杂的网络文件系统同步。
- **功能单一**: 它是纯算法库，不具备数据库的 Metadata 过滤（Payload Filtering）和权限管控能力。

## 2. Qdrant：云原生的向量数据库

Qdrant 将向量搜索从“算法”提升到了“服务”层面：
- **API 驱动**: 通过 gRPC/HTTP 与数据库通讯，Python 逻辑层彻底变为“无状态” (Stateless)。
- **Payload 过滤**: 在搜索向量的同时，可以指定 `model='gpt-4'` 等条件进行精准过滤。
- **水平扩展**: Qdrant 自身支持分片与副本，支撑网关的海量请求。

## 3. 架构迁移全景

### 3.1 核心数据流
`[User Prompt] -> [Sentence Transformer (Python)] -> [Vector Search (Qdrant)] -> [Logic Process]`

### 3.2 关键代码对比 (Python)

**旧版 (Faiss + Index File):**
```python
faiss.write_index(index, "vector.index")  # 强依赖本地磁盘
```

**新版 (Qdrant Client):**
```python
client.upsert(
    collection_name="cache",
    points=[PointStruct(id=id, vector=vector, payload={"response": resp})]
) # 进程间通讯，节点可弹性销毁
```

## 4. 最佳实践建议

1. **命名空间隔离**: 利用 Qdrant 的 `collection` 隔离不同的业务域或不同版本的 Embedding 模型。
2. **异步非阻塞**: 虽然在本演示中使用了同步客户端，但在生产环境下，建议在 Go 侧直接对接 Qdrant 减少一层 Python 透传。
3. **向量压缩**: 对于海量缓存，可以开启 Qdrant 的标量量化 (Scalar Quantization) 以节省 4 倍以上的内存消耗。

---
> [!TIP]
> 这一升级标志着 AI Gateway 正式具备了生产级的容灾能力。即使所有 Python 处理节点宕机重启，用户的语义缓存依然稳固在集中式数据库中。
