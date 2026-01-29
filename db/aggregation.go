package db

import (
	"context"
	"log"
	"ping-go/config"
	"ping-go/model"
	"time"
)

var aggregationCancel context.CancelFunc

// StartAggregationJob 启动数据聚合任务
// 该任务定期将原始心跳数据聚合为小时级和日级数据，并清理过期数据
func StartAggregationJob(ctx context.Context) {
	// 每小时执行一次聚合
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// 启动时立即执行一次聚合
	runAggregation()

	for {
		select {
		case <-ticker.C:
			runAggregation()
		case <-ctx.Done():
			log.Println("Aggregation job stopped")
			return
		}
	}
}

// runAggregation 执行完整的聚合流程
func runAggregation() {
	log.Println("Running heartbeat aggregation...")

	// 1. 聚合过去1小时的原始数据到 HeartbeatHourly
	aggregateHourly()

	// 2. 聚合昨天的小时数据到 HeartbeatDaily
	aggregateDaily()

	// 3. 清理超期数据
	cleanupAggregatedData()

	log.Println("Heartbeat aggregation completed")
}

// aggregateHourly 将原始心跳数据聚合为小时级
func aggregateHourly() {
	// 获取上一个完整小时的时间范围
	now := time.Now()
	hourEnd := now.Truncate(time.Hour)
	hourStart := hourEnd.Add(-1 * time.Hour)

	// 获取所有 monitor 的 ID
	var monitorIDs []uint
	DB.Model(&model.Monitor{}).Pluck("id", &monitorIDs)

	aggregatedCount := 0
	for _, monitorID := range monitorIDs {
		// 检查是否已聚合（避免重复聚合）
		var count int64
		DB.Model(&model.HeartbeatHourly{}).
			Where("monitor_id = ? AND hour = ?", monitorID, hourStart).
			Count(&count)
		if count > 0 {
			continue // 已聚合，跳过
		}

		// 使用 SQL 聚合查询获取统计数据
		// 注意：平均延迟只计算成功响应(status=1)的数据，去除失败响应的影响
		type AggResult struct {
			UpCount     int
			DownCount   int
			TotalCount  int
			SumDuration int64 // 成功响应的延迟总和
			MinDuration int
			MaxDuration int
		}
		var result AggResult

		DB.Model(&model.Heartbeat{}).
			Select(`
				SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END) as up_count,
				SUM(CASE WHEN status = 0 THEN 1 ELSE 0 END) as down_count,
				COUNT(*) as total_count,
				COALESCE(SUM(CASE WHEN status = 1 THEN duration ELSE 0 END), 0) as sum_duration,
				COALESCE(MIN(CASE WHEN status = 1 THEN duration ELSE NULL END), 0) as min_duration,
				COALESCE(MAX(CASE WHEN status = 1 THEN duration ELSE NULL END), 0) as max_duration
			`).
			Where("monitor_id = ? AND time >= ? AND time < ?",
				monitorID, hourStart, hourEnd).
			Scan(&result)

		if result.TotalCount == 0 {
			continue // 没有数据，跳过
		}

		// 计算可用率 (使用10000倍存储，0-10000 表示 0.00%-100.00%)
		uptime := 0
		if result.TotalCount > 0 {
			uptime = result.UpCount * 10000 / result.TotalCount
		}

		// 计算平均延迟（只基于成功响应）
		avgDuration := 0
		if result.UpCount > 0 {
			avgDuration = int(result.SumDuration) / result.UpCount
		}

		// 保存聚合结果
		hourly := model.HeartbeatHourly{
			MonitorID:   monitorID,
			Hour:        hourStart,
			UpCount:     result.UpCount,
			DownCount:   result.DownCount,
			TotalCount:  result.TotalCount,
			SumDuration: int(result.SumDuration), // 存储总和用于日聚合加权平均
			AvgDuration: avgDuration,
			MinDuration: result.MinDuration,
			MaxDuration: result.MaxDuration,
			Uptime:      uptime,
		}
		if err := DB.Create(&hourly).Error; err != nil {
			log.Printf("Failed to create hourly aggregation for monitor %d: %v", monitorID, err)
		} else {
			aggregatedCount++
		}
	}

	if aggregatedCount > 0 {
		log.Printf("Created %d hourly aggregations for hour %s", aggregatedCount, hourStart.Format("2006-01-02 15:04"))
	}
}

// aggregateDaily 将小时级数据聚合为日级
func aggregateDaily() {
	// 获取昨天的日期
	now := time.Now()
	today := now.Truncate(24 * time.Hour)
	yesterday := today.Add(-24 * time.Hour)

	var monitorIDs []uint
	DB.Model(&model.Monitor{}).Pluck("id", &monitorIDs)

	aggregatedCount := 0
	for _, monitorID := range monitorIDs {
		// 检查是否已聚合
		var count int64
		DB.Model(&model.HeartbeatDaily{}).
			Where("monitor_id = ? AND date = ?", monitorID, yesterday).
			Count(&count)
		if count > 0 {
			continue
		}

		// 从小时数据聚合
		// 使用 sum_duration 进行加权平均计算，确保平均延迟准确
		type AggResult struct {
			UpCount     int
			DownCount   int
			TotalCount  int
			SumDuration int64 // 成功响应的延迟总和
			MinDuration int
			MaxDuration int
		}
		var result AggResult

		DB.Model(&model.HeartbeatHourly{}).
			Select(`
				COALESCE(SUM(up_count), 0) as up_count,
				COALESCE(SUM(down_count), 0) as down_count,
				COALESCE(SUM(total_count), 0) as total_count,
				COALESCE(SUM(sum_duration), 0) as sum_duration,
				COALESCE(MIN(min_duration), 0) as min_duration,
				COALESCE(MAX(max_duration), 0) as max_duration
			`).
			Where("monitor_id = ? AND hour >= ? AND hour < ?",
				monitorID, yesterday, today).
			Scan(&result)

		if result.TotalCount == 0 {
			continue
		}

		// 计算可用率 (使用10000倍存储)
		uptime := 0
		if result.TotalCount > 0 {
			uptime = result.UpCount * 10000 / result.TotalCount
		}

		// 计算加权平均延迟（只基于成功响应）
		avgDuration := 0
		if result.UpCount > 0 {
			avgDuration = int(result.SumDuration) / result.UpCount
		}

		daily := model.HeartbeatDaily{
			MonitorID:   monitorID,
			Date:        yesterday,
			UpCount:     result.UpCount,
			DownCount:   result.DownCount,
			TotalCount:  result.TotalCount,
			SumDuration: int(result.SumDuration), // 存储总和
			AvgDuration: avgDuration,
			MinDuration: result.MinDuration,
			MaxDuration: result.MaxDuration,
			Uptime:      uptime,
		}
		if err := DB.Create(&daily).Error; err != nil {
			log.Printf("Failed to create daily aggregation for monitor %d: %v", monitorID, err)
		} else {
			aggregatedCount++
		}
	}

	if aggregatedCount > 0 {
		log.Printf("Created %d daily aggregations for date %s", aggregatedCount, yesterday.Format("2006-01-02"))
	}
}

// cleanupAggregatedData 清理超期的各级数据
func cleanupAggregatedData() {
	now := time.Now()
	retention := config.GlobalConfig.Retention

	// 获取保留时间配置（使用默认值如果未配置）
	rawHours := retention.RawHours
	if rawHours <= 0 {
		rawHours = 24 // 默认保留24小时原始数据
	}

	hourlyDays := retention.HourlyDays
	if hourlyDays <= 0 {
		hourlyDays = 7 // 默认保留7天小时数据
	}

	dailyDays := retention.DailyDays
	if dailyDays <= 0 {
		dailyDays = 365 // 默认保留1年日数据
	}

	// 清理原始心跳数据
	rawCutoff := now.Add(-time.Duration(rawHours) * time.Hour)
	result := DB.Where("time < ?", rawCutoff).Delete(&model.Heartbeat{})
	if result.Error != nil {
		log.Printf("Failed to cleanup raw heartbeats: %v", result.Error)
	} else if result.RowsAffected > 0 {
		log.Printf("Cleaned up %d raw heartbeats (older than %d hours)", result.RowsAffected, rawHours)
	}

	// 清理小时级数据
	hourlyCutoff := now.AddDate(0, 0, -hourlyDays)
	result = DB.Where("hour < ?", hourlyCutoff).Delete(&model.HeartbeatHourly{})
	if result.Error != nil {
		log.Printf("Failed to cleanup hourly heartbeats: %v", result.Error)
	} else if result.RowsAffected > 0 {
		log.Printf("Cleaned up %d hourly heartbeats (older than %d days)", result.RowsAffected, hourlyDays)
	}

	// 清理日级数据
	dailyCutoff := now.AddDate(0, 0, -dailyDays)
	result = DB.Where("date < ?", dailyCutoff).Delete(&model.HeartbeatDaily{})
	if result.Error != nil {
		log.Printf("Failed to cleanup daily heartbeats: %v", result.Error)
	} else if result.RowsAffected > 0 {
		log.Printf("Cleaned up %d daily heartbeats (older than %d days)", result.RowsAffected, dailyDays)
	}
}

// ForceAggregation 手动触发聚合（可用于 API 调用或迁移）
func ForceAggregation() {
	runAggregation()
}
