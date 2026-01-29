package db

import (
	"context"
	"fmt"
	"log"
	"ping-go/model"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var DB *gorm.DB

type HeartbeatBuffer struct {
	buffer chan *model.Heartbeat
	done   chan struct{}
}

const (
	HeartbeatBufferSize    = 1000
	HeartbeatBatchSize     = 100
	HeartbeatFlushInterval = 5 * time.Second
	HeartbeatFlushWaitTime = 500 * time.Millisecond
)

var (
	heartbeatBuffer *HeartbeatBuffer
	cleanupCancel   context.CancelFunc
)

func Init(dbPath string) error {
	var err error
	// Enable WAL mode
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	DB, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}

	// Optimize connection pool for SQLite
	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("failed to get sqlDB: %w", err)
	}
	// SQLite supports one writer at a time, but WAL allows concurrent readers.
	// However, GORM/database/sql manages connection pooling.
	// MaxOpenConns(1) is safest for simple SQLite usage to avoid "database is locked"
	// if we strictly want serialization, but with WAL we can increase it slightly.
	// Since we use a single writer routine (heartbeat buffer), we can set this higher for reads.
	sqlDB.SetMaxOpenConns(25) // Allow more for concurrent reads
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(1 * time.Hour)

	// Auto Migrate - 包含聚合表
	err = DB.AutoMigrate(
		&model.Monitor{},
		&model.User{},
		&model.Session{},
		&model.Setting{},
		&model.Notification{},
		&model.Heartbeat{},
		&model.HeartbeatHourly{},
		&model.HeartbeatDaily{},
	)
	if err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	// Init Buffer
	heartbeatBuffer = &HeartbeatBuffer{
		buffer: make(chan *model.Heartbeat, HeartbeatBufferSize),
		done:   make(chan struct{}),
	}
	go runHeartbeatBuffer(HeartbeatBatchSize, HeartbeatFlushInterval)

	// Start Aggregation Job (包含聚合和清理)
	ctx, cancel := context.WithCancel(context.Background())
	cleanupCancel = cancel
	go StartAggregationJob(ctx)

	return nil
}

func runHeartbeatBuffer(batchSize int, flushInterval time.Duration) {
	batch := make([]*model.Heartbeat, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case h := <-heartbeatBuffer.buffer:
			batch = append(batch, h)
			if len(batch) >= batchSize {
				flushHeartbeats(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				flushHeartbeats(batch)
				batch = batch[:0]
			}
		case <-heartbeatBuffer.done:
			if len(batch) > 0 {
				flushHeartbeats(batch)
			}
			return
		}
	}
}

func flushHeartbeats(batch []*model.Heartbeat) {
	if err := DB.CreateInBatches(batch, HeartbeatBatchSize).Error; err != nil {
		log.Printf("Failed to flush heartbeats: %v", err)
	}
}

func AddHeartbeat(h *model.Heartbeat) {
	if heartbeatBuffer == nil {
		log.Println("Heartbeat buffer not initialized, dropping")
		return
	}
	select {
	case heartbeatBuffer.buffer <- h:
	default:
		log.Println("Heartbeat buffer full, dropping")
	}
}

func FlushHeartbeatBuffer() {
	if heartbeatBuffer != nil {
		close(heartbeatBuffer.done)
		// Set to nil to prevent further writes in AddHeartbeat
		heartbeatBuffer = nil
	}
}

func Close() {
	log.Println("Closing database...")
	if cleanupCancel != nil {
		cleanupCancel()
	}
	FlushHeartbeatBuffer()

	// Wait a bit for buffer to flush
	time.Sleep(HeartbeatFlushWaitTime)

	sqlDB, err := DB.DB()
	if err == nil {
		sqlDB.Close()
	}
}
