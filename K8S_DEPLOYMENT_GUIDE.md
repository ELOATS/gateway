# AI 网关 Kubernetes / Minikube 本地部署指南

本文档用于指导你在本地 Minikube 中完整部署当前版本的 AI 网关，并包含监控资源的接入步骤。

## 1. 准备环境

请先安装以下工具：

- Docker Desktop
- `kubectl`
- `minikube`

推荐的本地集群启动参数：

```powershell
minikube start --cpus=4 --memory=8192
```

确认集群可用：

```powershell
kubectl get nodes
```

## 2. 在本地构建镜像

在仓库根目录执行：

```powershell
docker build -t ai-gateway-orchestration:latest -f core-go/Dockerfile .
docker build -t ai-gateway-intelligence:latest -f logic-python/Dockerfile .
docker build -t ai-gateway-nitro:latest -f utils-rust/Dockerfile .
```

## 3. 将镜像加载到 Minikube

```powershell
minikube image load ai-gateway-orchestration:latest
minikube image load ai-gateway-intelligence:latest
minikube image load ai-gateway-nitro:latest
```

如果需要确认镜像确实进入 Minikube，可以执行：

```powershell
minikube ssh -- docker images
```

## 4. 准备基础资源

先部署命名空间、密钥和持久卷声明：

```powershell
kubectl apply -f k8s/base.yaml
```

如果你要修改 API Key 或其他默认密钥，建议先编辑 `k8s/base.yaml` 再执行 apply。

## 5. 部署基础设施

部署 Redis：

```powershell
kubectl apply -f k8s/redis.yaml
```

等待基础设施启动完成：

```powershell
kubectl get pods -n ai-gateway -w
```

## 6. 部署 Nitro 和 Intelligence

```powershell
kubectl apply -f k8s/nitro.yaml
kubectl apply -f k8s/intelligence.yaml
```

继续观察 Pod 状态，直到 Rust 和 Python 服务都进入 `Running`：

```powershell
kubectl get pods -n ai-gateway -w
```

## 7. 部署 Go 编排层

```powershell
kubectl apply -f k8s/orchestration.yaml
```

等待网关 Pod 启动完成：

```powershell
kubectl get pods -n ai-gateway -w
```

## 8. 验证服务与健康状态

查看 Service：

```powershell
kubectl get svc -n ai-gateway
```

获取 Minikube 可访问地址：

```powershell
minikube service orchestration-service -n ai-gateway --url
```

然后发送一个最小测试请求：

```bash
curl -X POST http://<URL>/v1/chat/completions \
  -H "Authorization: Bearer sk-gw-default-123456" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello Minikube"}]}'
```

同时验证健康、就绪和指标接口：

```bash
curl http://<URL>/healthz
curl http://<URL>/readyz
curl http://<URL>/metrics
```

注意：`/readyz` 现在不再是固定返回 ready，而是会根据必需依赖的健康状态来决定返回结果。

## 9. 接入监控资源

这一步要求你的集群已经安装 Prometheus Operator 或兼容套件，例如 `kube-prometheus-stack`。

应用监控资源：

```powershell
kubectl apply -f k8s/servicemonitor.yaml
kubectl apply -f k8s/prometheus-rules.yaml
```

验证资源：

```powershell
kubectl describe servicemonitor ai-gateway-orchestration -n ai-gateway
kubectl describe prometheusrule ai-gateway-alerts -n ai-gateway
```

如需更详细的监控说明，请查看：

- [k8s/MONITORING.md](/D:/workspace/codes4/gateway/k8s/MONITORING.md)

## 10. 本地迭代开发流程

当你修改代码后，推荐使用下面的固定流程：

1. 重新构建受影响镜像
2. 重新加载到 Minikube
3. 重启对应 Deployment

### Go 改动

```powershell
docker build -t ai-gateway-orchestration:latest -f core-go/Dockerfile .
minikube image load ai-gateway-orchestration:latest
kubectl rollout restart deployment orchestration -n ai-gateway
```

### Python 改动

```powershell
docker build -t ai-gateway-intelligence:latest -f logic-python/Dockerfile .
minikube image load ai-gateway-intelligence:latest
kubectl rollout restart deployment intelligence -n ai-gateway
```

### Rust 改动

```powershell
docker build -t ai-gateway-nitro:latest -f utils-rust/Dockerfile .
minikube image load ai-gateway-nitro:latest
kubectl rollout restart deployment nitro -n ai-gateway
```

## 11. 常用排查命令

```powershell
kubectl get all -n ai-gateway
kubectl logs -f deployment/orchestration -n ai-gateway
kubectl logs -f deployment/intelligence -n ai-gateway
kubectl logs -f deployment/nitro -n ai-gateway
kubectl describe pod <pod-name> -n ai-gateway
```

## 12. 常见问题

### Minikube 中找不到镜像

重新执行：

```powershell
minikube image load <image>:latest
```

### Go 网关无法连接 Python 或 Rust

检查以下文件中的 Service 名称和环境变量是否一致：

- `k8s/orchestration.yaml`
- `k8s/intelligence.yaml`
- `k8s/nitro.yaml`

再查看 orchestration 日志：

```powershell
kubectl logs -f deployment/orchestration -n ai-gateway
```

### `/readyz` 返回 not ready

先查看返回内容：

```bash
curl http://<URL>/readyz
```

然后根据返回的依赖状态去检查对应 Pod 和 Service。

### `ServiceMonitor` 或 `PrometheusRule` 无法识别

通常说明集群中还没有安装 Prometheus Operator。

## 13. 一次性完整部署摘要

如果你想从头到尾完整跑一遍本地部署，可以直接按这个顺序执行：

```powershell
minikube start --cpus=4 --memory=8192

docker build -t ai-gateway-orchestration:latest -f core-go/Dockerfile .
docker build -t ai-gateway-intelligence:latest -f logic-python/Dockerfile .
docker build -t ai-gateway-nitro:latest -f utils-rust/Dockerfile .

minikube image load ai-gateway-orchestration:latest
minikube image load ai-gateway-intelligence:latest
minikube image load ai-gateway-nitro:latest

kubectl apply -f k8s/base.yaml
kubectl apply -f k8s/redis.yaml
kubectl apply -f k8s/nitro.yaml
kubectl apply -f k8s/intelligence.yaml
kubectl apply -f k8s/orchestration.yaml

# 可选：前提是已安装 Prometheus Operator
kubectl apply -f k8s/servicemonitor.yaml
kubectl apply -f k8s/prometheus-rules.yaml
```
