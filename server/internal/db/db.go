// Package db 负责数据库连接初始化与自动迁移。
// 驱动规则：PG_DSN 非空时使用 PostgreSQL（生产目标），
// 否则使用 SQLite（glebarez 纯 Go 驱动，无 CGO，本地开发验证用）。
package db

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"cmdb/server/internal/store"
)

// gormConfig 是统一的 GORM 配置：慢查询告警，忽略 record-not-found 噪音日志。
var gormConfig = &gorm.Config{
	Logger: gormlogger.New(log.New(os.Stdout, "", log.LstdFlags), gormlogger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  gormlogger.Warn,
		IgnoreRecordNotFoundError: true,
	}),
}

// Init 按配置初始化 GORM 连接并执行自动迁移。
// pgDSN 非空走 PostgreSQL；否则在 sqlitePath 打开 SQLite 文件库。
func Init(pgDSN, sqlitePath string) (*gorm.DB, error) {
	var (
		gdb *gorm.DB
		err error
	)
	if pgDSN != "" {
		gdb, err = gorm.Open(postgres.Open(pgDSN), gormConfig)
	} else {
		if sqlitePath == "" {
			sqlitePath = "./cmdb-dev.db"
		}
		gdb, err = gorm.Open(sqlite.Open(sqlitePath), gormConfig)
	}
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// 配置连接池参数。
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(20)                  // 最大打开连接数
	sqlDB.SetMaxIdleConns(5)                   // 最大空闲连接数
	sqlDB.SetConnMaxLifetime(30 * time.Minute) // 连接最大存活时间

	// 启动自动迁移全部实体。
	if err := gdb.AutoMigrate(store.AllModels()...); err != nil {
		return nil, fmt.Errorf("自动迁移失败: %w", err)
	}
	return gdb, nil
}

// Ping 检测数据库连通性。
func Ping(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}
