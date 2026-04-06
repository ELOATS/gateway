# Monitoring

本文档说明当前 Gateway 的监控接入重点，以及它与架构边界的关系。

## 1. 监控目标

监控不是为了采更多指标，而是为了回答几个稳定问题：

- 网关是否可用。
- 网关是否 ready。
- 关键依赖是否健康。
- 当前是否处于 degraded。
- 主链路时延、错误和失败模式是否异常。

## 2. 当前建议关注的对象

- `core-go` HTTP 服务可用性。
- `logic-python` gRPC 可达性。
- Rust Nitro 相关依赖状态。
- Redis 等共享依赖。
- 请求总量、错误率、延迟分布。
- 降级事件和依赖健康变化。

## 3. 接入方式

如果使用 Prometheus Operator，通常需要：

1. 部署网关工作负载并暴露 metrics。
2. 应用 `k8s/servicemonitor.yaml`。
3. 应用 `k8s/prometheus-rules.yaml`。
4. 在告警中区分“依赖不可用但允许 degraded”和“服务不可提供请求”。

## 4. 指标设计原则

当前系统已经把一部分运行态信息显式化，监控设计应遵守这些边界：

- 运行时状态由 observability / runtime 子系统统一表达。
- 业务模块只上报事件，不直接决定指标命名和生命周期。
- 依赖健康状态应可观察，但不要让指标直接替代业务降级逻辑。

## 5. 排查建议

如果监控显示异常，先判断是哪一类：

- 服务本身不可用。
- 服务可用但未 ready。
- 请求错误率升高。
- 某个依赖进入 degraded。
- proto 漂移或跨语言行为不一致导致的逻辑错误。

然后再回到：

- [TROUBLESHOOTING_GUIDE.md](/D:/workspace/codes4/gateway/TROUBLESHOOTING_GUIDE.md)
- [ARCHITECTURE_BOUNDARIES.md](/D:/workspace/codes4/gateway/core-go/ARCHITECTURE_BOUNDARIES.md)
