package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ai-gateway/core/internal/adapters"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/runtime"
	"github.com/redis/go-redis/v9"
)

// InitRuntimeStatus 先注册一组“启动中”的依赖状态。
// 后续各依赖初始化成功或降级后，再显式覆盖为最终状态。
func InitRuntimeStatus(cfg *config.Config) *runtime.SystemStatus {
	status := runtime.NewSystemStatus()
	status.Set(runtime.DependencyStatus{Name: "nitro", Required: true, Healthy: false, Status: "starting", FailureMode: cfg.NitroFailureMode})
	status.Set(runtime.DependencyStatus{Name: "python", Required: false, Healthy: false, Status: "starting", FailureMode: cfg.PythonInputFailureMode})
	status.Set(runtime.DependencyStatus{Name: "redis", Required: false, Healthy: false, Status: "starting", Version: cfg.RedisAddr})
	return status
}

// LoadDynamicPlugins 尝试加载基于 YAML 的动态 provider 配置。
func LoadDynamicPlugins(pluginDir string) {
	_ = os.MkdirAll(pluginDir, 0o755)
	if err := adapters.GlobalRegistry.LoadPlugins(pluginDir); err != nil {
		slog.Warn("failed to load dynamic plugins", "dir", pluginDir, "error", err)
		return
	}
	slog.Info("dynamic plugins loaded", "count", len(adapters.GlobalRegistry.Plugins))
}

// InitRedis 初始化 Redis 客户端并同步更新依赖状态。
// 对当前系统来说 Redis 属于可降级基础设施，因此失败只会标记 degraded。
func InitRedis(cfg *config.Config, status *runtime.SystemStatus) *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	if err := rdb.Ping(context.Background()).Err(); err != nil {
		slog.Warn("redis unavailable; distributed rate limiting will degrade", "error", err)
		status.Set(runtime.DependencyStatus{
			Name:    "redis",
			Healthy: false,
			Status:  "degraded",
			Reason:  err.Error(),
			Version: cfg.RedisAddr,
		})
		return rdb
	}

	slog.Info("redis connected", "addr", cfg.RedisAddr)
	status.Set(runtime.DependencyStatus{
		Name:    "redis",
		Healthy: true,
		Status:  "ready",
		Version: cfg.RedisAddr,
	})
	return rdb
}
