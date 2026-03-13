# 🚀 AI 网关部署指南 (Kubernetes / Minikube)

本指南旨在指导开发者在本地 **minikube** 环境中快速部署并运行 AI 网关。系统采用 **Go (编排层)**、**Rust (加速层)** 与 **Python (智能层)** 的三层平面架构，通过 gRPC 实现高性能通信。

---

## 🏗️ 1. 系统架构概览

| 平面 (Plane) | 技术栈 | 服务名称 (K8s Service) | 职责 |
| :--- | :--- | :--- | :--- |
| **编排层 (Orchestration)** | **Go** | `orchestration-service` | 入口、路由、可观测性、身份验证 |
| **加速层 (Nitro)** | **Rust** | `nitro-service` | PII 脱敏、Token 计数 (高性能正则/分词) |
| **智能层 (Intelligence)** | **Python** | `intelligence-service` | 语义缓存 (FAISS)、安全审计、幻觉检测 |

---

## 🛠️ 2. 环境准备

1.  **基础设施**: 安装 [minikube](https://minikube.sigs.k8s.io/docs/start/)、[kubectl](https://kubernetes.io/docs/tasks/tools/) 和 Docker Desktop。
2.  **Docker 镜像加速**: 在 Docker Desktop 中配置 `registry-mirrors` 以加速基础镜像下载。
3.  **启动 Minikube**:
    ```powershell
    minikube start --cpus=4 --memory=8192
    ```

---

## ⚙️ 3. 配置管理 (.env)

在项目根目录创建 `.env` 文件（由 `.gitignore` 保护，不提交）：

```bash
# --- 统一配置 ---
PORT=8080
GATEWAY_API_KEY=sk-gw-default-123456
OPENAI_API_KEY=sk-xxxx...  # 填入你的真实 Key 以启用转发

# --- 内部 gRPC 地址 (K8s 内部域名) ---
PYTHON_ADDR=intelligence-service:50051
RUST_ADDR=nitro-service:50052
```

---

## 📦 4. 构建与加载镜像 (SOP)

由于 minikube 内部网络环境复杂，我们采用 **“本地构建 -> 物理加载”** 的策略：

### Step 1: 本地构建 (Local Build)
在普通终端（未挂载 `docker-env`）下运行：

```powershell
# 1. 构建编排层 (Go)
docker build -t ai-gateway-orchestration:latest -f core-go/Dockerfile .

# 2. 构建智能层 (Python - 已优化为 CPU 版，约 500MB)
docker build -t ai-gateway-intelligence:latest -f logic-python/Dockerfile .

# 3. 构建加速层 (Rust - 使用 1.85+ 镜像支持 Edition 2024)
docker build -t ai-gateway-nitro:latest -f utils-rust/Dockerfile .
```

### Step 2: 加载镜像到 Minikube
将本地镜像直接推送到 minikube 虚拟机节点：

```powershell
minikube image load ai-gateway-orchestration:latest
minikube image load ai-gateway-intelligence:latest
minikube image load ai-gateway-nitro:latest
```

---

## ☸️ 5. Kubernetes 部署

按照依赖顺序应用资源清单：

```powershell
# 1. 部署基础配置 (Namespace, Secret, PVC)
kubectl apply -f k8s/base.yaml

# 2. 部署后端微服务 (Rust & Python)
kubectl apply -f k8s/nitro.yaml
kubectl apply -f k8s/intelligence.yaml

# 3. 部署入口网关 (Go)
kubectl apply -f k8s/orchestration.yaml
```

---

## 🔍 6. 验证与访问

1.  **检查 Pod 状态**:
    ```powershell
    kubectl get pods -n ai-gateway -w
    ```
    *期望所有 Pod 最终显示为 `Running`。*

2.  **获取访问 URL**:
    ```powershell
    minikube service orchestration-service -n ai-gateway --url
    ```
    *示例返回: `http://192.168.49.2:30080`*

3.  **发送测试请求**:
    ```bash
    curl -X POST http://<URL>/v1/chat/completions \
      -H "Authorization: Bearer sk-gw-default-123456" \
      -H "Content-Type: application/json" \
      -d '{"model":"gpt-4o", "messages":[{"role":"user","content":"Hello K8s!"}]}'
    ```

---

## 🛡️ 7. 常见问题排查 (Troubleshooting)

| 问题现象 | 可能原因 | 解决方法 |
| :--- | :--- | :--- |
| `ErrImageNeverPull` | 镜像未成功加载到 minikube | 重新运行 `minikube image load <镜像名>` |
| Python 层构建极慢 | 正在下载 CUDA 依赖 | 检查 `uv.lock` 是否包含 `nvidia-*`，确保 `pyproject.toml` 包含 `pytorch-cpu` 索引。 |
| Rust 层构建报错 `edition2024` | 基础镜像版本太低 | 确保 `utils-rust/Dockerfile` 使用 `FROM rust:1.85-slim`。 |
| Go 报错 `connection refused` | 内部 Service 域名无法解析 | 检查 `k8s/*.yaml` 中的 `Service` 名称是否与 Go 的 `ENV` 配置一致。 |
| 无法访问外网 OpenAI | minikube 虚拟机没有外网访问权 | 检查 minikube 网络插件或配置 http 代理。 |

---

## 🔄 8. 迭代开发与验证 (Iterative Development)

当你修改了代码（增加功能或修复 Bug）并希望在 minikube 中验证时，请根据修改范围选择对应的流程：

### 场景 A：修改了业务逻辑 (最常用)
*修改了 Rust 的正则、Python 的阈值或 Go 的路由算法。*
1.  **重新构建镜像**: `docker build -t <镜像名>:latest -f <Dockerfile路径> .`
2.  **重新加载镜像**: `minikube image load <镜像名>:latest`
3.  **触发滚动更新**: `kubectl rollout restart deployment <Deployment名> -n ai-gateway`
    *   例如: `kubectl rollout restart deployment nitro -n ai-gateway`

### 场景 B：修改了通信协议 (proto)
*增加了 gRPC 接口或修改了消息字段。*
1.  **生成代码**: `make proto`
2.  **构建并加载所有受影响镜像**: (同场景 A)
3.  **重启所有服务**: `kubectl rollout restart deployment -n ai-gateway`

### 场景 C：修改了配置或密钥
1.  **更新 YAML**: 修改 `k8s/base.yaml` 或 `k8s/orchestration.yaml`。
2.  **应用配置**: `kubectl apply -f <文件名>`
3.  **重启服务**: `kubectl rollout restart deployment orchestration -n ai-gateway`

### 💡 高效调试技巧
- **实时日志**: `kubectl logs -f -l app=<app名> -n ai-gateway`
- **本地优先**: 在上云前，建议先在本地直接运行各平面（Go/Python/Rust）进行逻辑验证。

---

*AI 网关：高性能、多语言、安全可控。*
