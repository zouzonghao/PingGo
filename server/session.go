package server

import (
	"crypto/rand"
	"encoding/hex"
	"ping-go/db"
	"ping-go/model"
	"ping-go/pkg/logger"
	"sync"
	"time"

	"go.uber.org/zap"
)

// socketAuth 存储 socket 连接的认证状态
// key: socketID (string), value: map[string]any
var socketAuth = sync.Map{}

// startSessionCleanup 启动会话清理任务，每小时清理过期的会话
func startSessionCleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	for range ticker.C {
		// Clean up expired sessions from DB
		if err := db.DB.Where("expires_at < ?", time.Now()).Delete(&model.Session{}).Error; err != nil {
			logger.Error("Failed to clean up sessions", zap.Error(err))
		}
	}
}

// generateToken 生成一个安全的随机 token
func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
