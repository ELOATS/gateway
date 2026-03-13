# AI 网关：系统架构白皮书与技术规格说明 (V5 - 全量打磨版)

欢迎加入 AI 网关研发团队。本系统是一个典型的**多语言微服务协作系统**，通过 Go、Rust 和 Python 的深度集成，构建了一套兼具高并发、高性能与高智能的 AI 治理方案。

---

## 📖 1. 设计哲学：多语言三层架构的必然性

在设计之初，我们对比了单一技术栈的局限性，
- **纯 Python**: 无法支撑万级并发，分词与正则性能在高吞吐下会成为瓶颈。
- **纯 Go**: 缺乏成熟的向量检索和复杂的 Transformer 库支持。
- **纯 Rust**: 开发业务逻辑（如多变的缓存策略）成本过高。
最终采用 **"分层治理，各取所长" (Best Tool for Each Plane)** 的策略：

| 平面 (Plane) | 技术栈 | 角色 | 选型理由 |
| :--- | :--- | :--- | :--- |
| **编排层 (Orchestration)** | **Go** | 交通枢纽 | 卓越的网络 I/O 处理能力和 Goroutine 并发模型，适合作为流量调度与聚合中心。 |
| **加速层 (Nitro)** | **Rust** | 动力引擎 | 零成本抽象、无 GC 暂停。在 Token 分词和正则匹配上，性能高出 Python 数个量级。 |
| **智能层 (Intelligence)** | **Python** | 推理大脑 | 拥有最顶级的 AI 生态（Transformers, Faiss），能毫秒级实现复杂的语义匹配。 |

---

## 🏗 2. 系统核心组件深度解析 (基于 Go 标准布局)

系统遵循 **Standard Go Project Layout**，旨在通过目录结构强制执行内部逻辑的封装。

### 2.1 编排层 (core-go)
- **`cmd/gateway/main.go`**: **系统的骨架**。仅负责加载环境变量、初始化 gRPC 连接池及启动服务。
- **`internal/handlers/`**: **业务编排**。`ChatHandler` 通过依赖注入协调全链路流程。
- **`internal/routes/`**: **路标中心**。集中定义路由、版本控制及中间件绑定（Auth、Metric）。
- **`internal/observability/`**: **全链路探针**。定义指标维度及初始化 JSON 结构化日志。
- **`pkg/models/`**: **公共协议**。定义所有平面共享的 DTO 结构。
- **`api/gateway/v1/`**: **通讯录**。存放自动生成的 gRPC Stub 代码。

### 2.2 智能层 (logic-python)
- **向量化引擎 (`SentenceTransformer`)**: 采用 `all-MiniLM-L6-v2` 384 维模型。
- **内存索引 (`Faiss`)**: 采用 L2 空间距离算法，实现毫秒级语义相似度搜索。

### 2.3 加速层 (utils-rust)
- **分词引擎 (`Tiktoken`)**: 集成 OpenAI BPE 算法，针对不同模型动态切换编码方案。
- **正则引擎**: 利用 Rust 编译器优化，实现毫秒级敏感信息（PII）脱敏。

---

## 🔄 3. 全链路请求追踪：一个 Prompt 的一生

了解请求的生命周期是每一位开发者的必修课：

1.  **准入 (Go)**: `AuthRequired` 拦截器检查 Bearer Token 并生成唯一的 **Request-ID (RID)**。                                           │
2.  **可观测性初始化 (Go)**: `slog` 打印第一条日志 `{"msg": "Incoming request", "request_id": "..."}`。                                 │
3.  **异步计费启动 (Rust)**: Go 开启 `goroutine` 异步发送原文至 Rust。Rust 提取 Metadata 中的 `x-request-id` 并计算 Token 消耗。        │
4.  **语义检索 (Python)**: Go 调用 Python 的 `GetCache`。Python 将文本 Embedding 后在 Faiss 空间中进行扫描。若命中，Go 直接结束请求。   │
5.  **命中: 返回 `hit=true` 及其内容，Go 直接结束请求，极大节省成本。                                                                     │
6.  **未命中: 继续后续流程。                                                                                                              │
7.  **Nitro 加速脱敏 (Rust)**: 若未命中缓存，Rust `CheckInput` 启动，利用正则 DFA 状态机瞬间抹除敏感字段。                              │
8.  **深度安全审计 (Python)**: 经过 Rust 初筛的文本进入 Python，检查是否存在“提示词注入”风险。                                          │
9.  **动态路由决策 (Go)**: `router` 包基于 **加权轮询算法** 算出本次请求的最佳供应商（如主节点 80% 流量）。                             │
10.  **上游代理 (Go)**: `adapters` 执行真实的 HTTPS 调用。                                                                               │
11.  **输出幻觉检测 (Python)**: 模型返回结果后，Python `CheckOutput` 介入，通过启发式策略检查响应中是否存在逻辑自相矛盾的内容。          │
12.  **响应回执 (Go)**: 最终结果返回时，Go 更新 Prometheus 延迟直方图并返回结果。

---

## 🛠 4. 实战扩展：如何优雅地增加功能？

### A. 添加模型提供商 (如 Claude)
在 `internal/adapters/` 中实现 `Provider` 接口，并在 `main.go` 中注册新 Candidate。

### B. 进阶案例：实现“每日消费配额”中间件
**场景**: 需要限制每个 API Key 每天只能消耗 100,000 个 Token。
1.  **解耦设计**: 在 `internal/middleware/` 下新建 `quota.go`。
2.  **挂载**: 在 `internal/routes/routes.go` 中通过 `.Use(middleware.QuotaLimiter())` 插入。
3.  **价值**: 实现了功能的“热插拔”，核心业务代码无需任何修改。

---

## 🐞 5. 故障排查与修复标准流程 (SOP)

### 5.1 快速隔离 (Isolation)
- **RID 追踪**: 在日志中搜索特定的 `request_id`，观察请求在哪个平面发生断点。
- **gRPC 错误**: 若出现 `DeadlineExceeded`，说明后端处理超时（通常是 Python 层的 Embedding 推理过慢）。

### 5.2 深度案例：排查“语义缓存漂移” (误命中)
**故障现象**: 用户问“写个 Python 脚本”，系统返回了“Java 冒泡排序”的缓存结果。
1.  **排查**: 通过 RID 发现 Python 日志显示 `Semantic Cache HIT`，但 `Distance: 0.19` 极其接近 `0.2` 的阈值。
2.  **根因**: 向量模型对相似意图的短句投影过近，导致语义重叠。
3.  **修复**:
     - **紧急**: 将 `main.py` 中的阈值收紧至 `0.1`。
     - **根治**: 修改 `GetCache` 协议，使缓存 Key 包含 `model` 类型，实现“物理隔离的语义缓存”。

---

## 🚀 6. 性能基准测试 (Benchmarking)

### 6.1 性能预期
- **网关开销 (Overhead)**: 纯网关处理时间（含向量计算）应控制在 **100ms** 以内。
- **Rust 处理能力**: 正则脱敏应在 **< 1ms** 内完成。

### 6.2 推荐测试工具
使用 `hey` 进行压力测试：
```powershell
hey -n 1000 -c 100 -m POST -H "Authorization: Bearer <key>" -d '{"model":"mock"}' http://localhost:8080/v1/chat/completions
```

---

## ⚙️ 7. 开发环境与命令速查

### gRPC 代码生成
```powershell
# Go
protoc --proto_path=proto --go_out=core-go --go-grpc_out=core-go proto/gateway.proto
# Python
cd logic-python; uv run python -m grpc_tools.protoc -I ../proto --python_out=. --grpc_python_out=. ../proto/gateway.proto
```

### 依赖同步
- Go: `go mod tidy`
- Python: `uv sync`
- Rust: `cargo build`

---

*“代码只能体现功能，而文档体现的是灵魂。”*
