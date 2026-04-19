package db

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/ai-gateway/core/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// GlobalDB 是全局可访问的 GORM 数据库连接实例单例。
var GlobalDB *gorm.DB

// InitDB 初始化持久化层连接，并执行全自动的数据库 Schema 迁移。
// 它内置了数据库选型逻辑：优先根据 DSN 协议头区分 PostgreSQL 与 SQLite，
// 这种双模支持确保了开发者可以在本地零配置启动（SQLite），而在生产环境对接高性能集群（Postgres）。
func InitDB(cfg *config.Config) error {
	var db *gorm.DB
	var err error

	dsn := cfg.DatabaseDSN
	if dsn == "" {
		dsn = "gateway.db"
	}

	if dsn == "gateway.db" || strings.HasSuffix(dsn, ".db") || !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "host=") {
		slog.Info("Using SQLite database", "dsn", dsn)
		db, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	} else {
		slog.Info("Using PostgreSQL database")
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	}

	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}

	// 执行自动迁移 (Auto Migration)
	// 设计原则：
	// 1. 声明式同步：通过 GORM 定义的 Tag 自动维护数据库表结构，确保代码与 Schema 的原子性一致。
	// 2. 演进稳定性：在多租户模型 Price 表、配额 Quota 表等核心资产变更时，能够平滑过渡且不丢失业务数据。
	err = db.AutoMigrate(&Tenant{}, &APIKey{}, &Quota{}, &UsageLog{}, &ModelPrice{})
	if err != nil {
		return fmt.Errorf("failed to auto migrate: %w", err)
	}

	GlobalDB = db
	return nil
}
