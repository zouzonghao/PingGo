package db

import (
	"ping-go/config"
	"ping-go/model"
	"time"
)

// GetHeartbeatsWithTimeRange 根据时间范围智能选择合适的数据源
// 这是分层查询的核心函数，自动根据查询时间范围选取最优数据源：
// - 24小时内: 查询原始心跳数据 (最高精度)
// - 7天内: 查询小时级聚合数据
// - 7天以上: 查询日级聚合数据
func GetHeartbeatsWithTimeRange(monitorID uint, hours int) ([]map[string]any, string) {
	retention := config.GlobalConfig.Retention

	rawHours := retention.RawHours
	if rawHours <= 0 {
		rawHours = 24
	}

	hourlyDays := retention.HourlyDays
	if hourlyDays <= 0 {
		hourlyDays = 7
	}

	if hours <= rawHours {
		// 原始数据
		return getRawHeartbeats(monitorID, hours), "raw"
	} else if hours <= hourlyDays*24 {
		// 小时聚合数据
		return getHourlyHeartbeats(monitorID, hours), "hourly"
	} else {
		// 日聚合数据
		return getDailyHeartbeats(monitorID, hours), "daily"
	}
}

// getRawHeartbeats 获取原始心跳数据
func getRawHeartbeats(monitorID uint, hours int) []map[string]any {
	var heartbeats []model.Heartbeat
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	DB.Where("monitor_id = ? AND time > ?", monitorID, cutoff).
		Order("time DESC").
		Find(&heartbeats)

	results := make([]map[string]any, len(heartbeats))
	for i, h := range heartbeats {
		results[i] = map[string]any{
			"monitorID": h.MonitorID,
			"status":    h.Status,
			"msg":       h.Message,
			"time":      h.Time.Format(time.RFC3339),
			"duration":  h.Duration,
			"type":      "raw",
		}
	}
	return results
}

// getHourlyHeartbeats 获取小时级聚合数据
func getHourlyHeartbeats(monitorID uint, hours int) []map[string]any {
	var heartbeats []model.HeartbeatHourly
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	DB.Where("monitor_id = ? AND hour > ?", monitorID, cutoff).
		Order("hour DESC").
		Find(&heartbeats)

	results := make([]map[string]any, len(heartbeats))
	for i, h := range heartbeats {
		// 根据可用率计算等效状态 (5000 = 50%)
		status := model.StatusUp
		if h.Uptime < 5000 {
			status = model.StatusDown
		}

		results[i] = map[string]any{
			"monitorID":   h.MonitorID,
			"status":      status,
			"time":        h.Hour.Format(time.RFC3339),
			"duration":    h.AvgDuration,
			"uptime":      float64(h.Uptime) / 100.0, // 转换为百分比显示
			"upCount":     h.UpCount,
			"downCount":   h.DownCount,
			"totalCount":  h.TotalCount,
			"minDuration": h.MinDuration,
			"maxDuration": h.MaxDuration,
			"type":        "hourly",
		}
	}
	return results
}

// getDailyHeartbeats 获取日级聚合数据
func getDailyHeartbeats(monitorID uint, hours int) []map[string]any {
	days := hours / 24
	if days < 1 {
		days = 1
	}

	var heartbeats []model.HeartbeatDaily
	cutoff := time.Now().AddDate(0, 0, -days)
	DB.Where("monitor_id = ? AND date > ?", monitorID, cutoff).
		Order("date DESC").
		Find(&heartbeats)

	results := make([]map[string]any, len(heartbeats))
	for i, h := range heartbeats {
		status := model.StatusUp
		if h.Uptime < 5000 {
			status = model.StatusDown
		}

		results[i] = map[string]any{
			"monitorID":   h.MonitorID,
			"status":      status,
			"time":        h.Date.Format(time.RFC3339),
			"duration":    h.AvgDuration,
			"uptime":      float64(h.Uptime) / 100.0, // 转换为百分比显示
			"upCount":     h.UpCount,
			"downCount":   h.DownCount,
			"totalCount":  h.TotalCount,
			"minDuration": h.MinDuration,
			"maxDuration": h.MaxDuration,
			"type":        "daily",
		}
	}
	return results
}

// GetUptimeStats 获取指定时间范围的可用率统计
// 使用真实的 UpCount/TotalCount 计算，更加精确
func GetUptimeStats(monitorID uint, duration time.Duration) float64 {
	hours := int(duration.Hours())
	now := time.Now()
	since := now.Add(-duration)
	currentHour := now.Truncate(time.Hour)

	retention := config.GlobalConfig.Retention
	rawHours := retention.RawHours
	if rawHours <= 0 {
		rawHours = 24
	}
	hourlyDays := retention.HourlyDays
	if hourlyDays <= 0 {
		hourlyDays = 7
	}

	if hours <= rawHours {
		// 原始数据范围内：直接从 Heartbeat 表精确计算
		var totalCount, upCount int64
		DB.Model(&model.Heartbeat{}).
			Where("monitor_id = ? AND time >= ?", monitorID, since).
			Count(&totalCount)

		if totalCount == 0 {
			return 100.0 // 无数据时默认100%
		}

		DB.Model(&model.Heartbeat{}).
			Where("monitor_id = ? AND time >= ? AND status = ?", monitorID, since, model.StatusUp).
			Count(&upCount)

		return float64(upCount) / float64(totalCount) * 100.0
	}

	// 超过原始数据保留时间：从聚合数据 + 当前小时原始数据计算
	// 1. 从小时聚合表获取已聚合数据的统计
	var hourlyUpCount, hourlyTotalCount int64
	DB.Model(&model.HeartbeatHourly{}).
		Where("monitor_id = ? AND hour >= ? AND hour < ?", monitorID, since, currentHour).
		Select("COALESCE(SUM(up_count), 0), COALESCE(SUM(total_count), 0)").
		Row().Scan(&hourlyUpCount, &hourlyTotalCount)

	// 2. 从原始表获取当前小时（未聚合）的数据
	var currentUpCount, currentTotalCount int64
	DB.Model(&model.Heartbeat{}).
		Where("monitor_id = ? AND time >= ?", monitorID, currentHour).
		Count(&currentTotalCount)
	DB.Model(&model.Heartbeat{}).
		Where("monitor_id = ? AND time >= ? AND status = ?", monitorID, currentHour, model.StatusUp).
		Count(&currentUpCount)

	// 3. 合并计算
	totalUp := hourlyUpCount + currentUpCount
	totalCount := hourlyTotalCount + currentTotalCount

	if totalCount == 0 {
		return 100.0
	}

	return float64(totalUp) / float64(totalCount) * 100.0
}

// GetAvgResponseTime 获取指定时间范围的平均响应时间
// 只统计成功响应(status=1)的延迟数据
func GetAvgResponseTime(monitorID uint, duration time.Duration) float64 {
	since := time.Now().Add(-duration)
	hours := int(duration.Hours())

	retention := config.GlobalConfig.Retention
	rawHours := retention.RawHours
	if rawHours <= 0 {
		rawHours = 24
	}
	hourlyDays := retention.HourlyDays
	if hourlyDays <= 0 {
		hourlyDays = 7
	}

	if hours <= rawHours {
		// 原始数据：只统计成功响应(status=1)的延迟
		var avg float64
		DB.Model(&model.Heartbeat{}).
			Where("monitor_id = ? AND time >= ? AND status = ? AND duration > 0", monitorID, since, model.StatusUp).
			Select("COALESCE(AVG(duration), 0)").
			Row().Scan(&avg)
		return avg
	} else if hours <= hourlyDays*24 {
		// 小时聚合数据：使用加权平均（sum_duration / up_count）
		var sumDuration, upCount int64
		DB.Model(&model.HeartbeatHourly{}).
			Where("monitor_id = ? AND hour >= ?", monitorID, since).
			Select("COALESCE(SUM(sum_duration), 0), COALESCE(SUM(up_count), 0)").
			Row().Scan(&sumDuration, &upCount)
		if upCount > 0 {
			return float64(sumDuration) / float64(upCount)
		}
		return 0
	} else {
		// 日聚合数据：使用加权平均
		days := hours / 24
		cutoff := time.Now().AddDate(0, 0, -days)
		var sumDuration, upCount int64
		DB.Model(&model.HeartbeatDaily{}).
			Where("monitor_id = ? AND date >= ?", monitorID, cutoff).
			Select("COALESCE(SUM(sum_duration), 0), COALESCE(SUM(up_count), 0)").
			Row().Scan(&sumDuration, &upCount)
		if upCount > 0 {
			return float64(sumDuration) / float64(upCount)
		}
		return 0
	}
}

// ChartDataPoint 图表数据点结构
// 用于返回给前端的图表数据
type ChartDataPoint struct {
	Time     string  `json:"time"`     // 时间标签
	Duration int     `json:"duration"` // 响应时间（毫秒）
	Status   int     `json:"status"`   // 状态 1=正常 0=异常
	Uptime   float64 `json:"uptime"`   // 可用率（0-100），仅聚合数据有
	IsLive   bool    `json:"isLive"`   // 是否是实时数据（最近未聚合的点）
}

// GetChartData 获取图表数据
// 支持两种视图：
// - "24h": 24个小时采样点，每个点代表1小时的聚合数据
// - "7d":  28个采样点，每个点代表6小时的聚合数据
// 最近的一个点（当前小时）由于还未聚合，从原始数据获取
func GetChartData(monitorID uint, view string) []ChartDataPoint {
	now := time.Now()
	currentHour := now.Truncate(time.Hour)

	if view == "24h" {
		// 24小时视图：24个小时采样点
		return getChartData24h(monitorID, now, currentHour)
	} else if view == "7d" {
		// 7天视图：28个6小时采样点
		return getChartData7d(monitorID, now, currentHour)
	}

	// 默认返回24小时视图
	return getChartData24h(monitorID, now, currentHour)
}

// getChartData24h 获取24小时图表数据
// 从HeartbeatHourly表获取过去23个完整小时的数据
// 最近1小时从原始Heartbeat表计算
func getChartData24h(monitorID uint, now time.Time, currentHour time.Time) []ChartDataPoint {
	// 计算时间范围：过去24小时
	startTime := currentHour.Add(-23 * time.Hour)

	// 1. 获取过去23个完整小时的聚合数据
	var hourlyData []model.HeartbeatHourly
	DB.Where("monitor_id = ? AND hour >= ? AND hour < ?", monitorID, startTime, currentHour).
		Order("hour ASC").
		Find(&hourlyData)

	// 2. 创建时间到数据的映射，方便查找
	hourlyMap := make(map[string]model.HeartbeatHourly)
	for _, h := range hourlyData {
		key := h.Hour.Format("2006-01-02 15:00")
		hourlyMap[key] = h
	}

	// 3. 构建24个数据点
	points := make([]ChartDataPoint, 24)

	// 填充前23个点（从聚合数据）
	for i := 0; i < 23; i++ {
		hourTime := startTime.Add(time.Duration(i) * time.Hour)
		key := hourTime.Format("2006-01-02 15:00")

		if data, exists := hourlyMap[key]; exists {
			// 有聚合数据
			status := model.StatusUp
			if data.Uptime < 5000 {
				status = model.StatusDown
			}
			points[i] = ChartDataPoint{
				Time:     hourTime.Format(time.RFC3339),
				Duration: data.AvgDuration,
				Status:   int(status),
				Uptime:   float64(data.Uptime) / 100.0, // 转换为百分比
				IsLive:   false,
			}
		} else {
			// 无数据，填充空点
			points[i] = ChartDataPoint{
				Time:     hourTime.Format(time.RFC3339),
				Duration: 0,
				Status:   -1, // 表示无数据
				Uptime:   100,
				IsLive:   false,
			}
		}
	}

	// 4. 最后一个点（当前小时）从原始数据获取
	points[23] = getCurrentHourPoint(monitorID, currentHour, now)

	return points
}

// getChartData7d 获取7天图表数据
// 每6小时合并为1个采样点，共28个点
// 最近1个点从原始数据获取
func getChartData7d(monitorID uint, now time.Time, currentHour time.Time) []ChartDataPoint {
	// 计算时间范围：过去7天 = 168小时
	// 每6小时一个点 = 28个点
	// 从当前往前推168小时，然后按6小时分组
	startTime := currentHour.Add(-167 * time.Hour)

	// 1. 获取过去168小时的小时聚合数据
	var hourlyData []model.HeartbeatHourly
	DB.Where("monitor_id = ? AND hour >= ? AND hour < ?", monitorID, startTime, currentHour).
		Order("hour ASC").
		Find(&hourlyData)

	// 2. 创建时间到数据的映射
	hourlyMap := make(map[string]model.HeartbeatHourly)
	for _, h := range hourlyData {
		key := h.Hour.Format("2006-01-02 15:00")
		hourlyMap[key] = h
	}

	// 3. 构建28个数据点（每6小时一个点）
	points := make([]ChartDataPoint, 28)

	// 填充前27个点（每个点是6个小时的聚合）
	for i := 0; i < 27; i++ {
		// 计算这个采样点覆盖的6小时时间范围
		slotStartTime := startTime.Add(time.Duration(i*6) * time.Hour)
		slotEndTime := slotStartTime.Add(6 * time.Hour)

		// 收集这6小时内的所有数据
		var totalDuration int
		var totalUpCount, totalDownCount, hourCount int

		for h := slotStartTime; h.Before(slotEndTime); h = h.Add(time.Hour) {
			key := h.Format("2006-01-02 15:00")
			if data, exists := hourlyMap[key]; exists {
				totalDuration += data.AvgDuration
				totalUpCount += data.UpCount
				totalDownCount += data.DownCount
				hourCount++
			}
		}

		if hourCount > 0 {
			// 计算6小时平均值
			avgDuration := totalDuration / hourCount
			uptime := float64(totalUpCount) / float64(totalUpCount+totalDownCount) * 100
			status := model.StatusUp
			if uptime < 50 {
				status = model.StatusDown
			}

			points[i] = ChartDataPoint{
				Time:     slotStartTime.Format(time.RFC3339),
				Duration: avgDuration,
				Status:   int(status),
				Uptime:   uptime, // 已经是百分比
				IsLive:   false,
			}
		} else {
			// 无数据
			points[i] = ChartDataPoint{
				Time:     slotStartTime.Format(time.RFC3339),
				Duration: 0,
				Status:   -1,
				Uptime:   100,
				IsLive:   false,
			}
		}
	}

	// 4. 最后一个点（当前6小时时段）从原始数据 + 已聚合数据合并
	points[27] = getCurrentSlotPoint(monitorID, currentHour, now, 6)

	return points
}

// getCurrentHourPoint 获取当前小时的实时数据点
// 从原始Heartbeat表读取当前小时内的最新一条记录
func getCurrentHourPoint(monitorID uint, currentHour time.Time, now time.Time) ChartDataPoint {
	// 查询当前小时内的原始数据
	var heartbeats []model.Heartbeat
	DB.Where("monitor_id = ? AND time >= ?", monitorID, currentHour).
		Order("time DESC").
		Find(&heartbeats)

	if len(heartbeats) == 0 {
		// 没有数据，返回空点
		return ChartDataPoint{
			Time:     now.Format(time.RFC3339),
			Duration: 0,
			Status:   -1,
			Uptime:   100,
			IsLive:   true,
		}
	}

	// 计算当前小时的平均响应时间（只统计成功响应）和可用率
	var totalDuration int
	var upCount, downCount int
	for _, h := range heartbeats {
		// 只统计成功响应的延迟
		if h.Status == model.StatusUp {
			totalDuration += h.Duration
			upCount++
		} else if h.Status == model.StatusDown {
			downCount++
		}
	}

	avgDuration := 0
	if upCount > 0 {
		avgDuration = totalDuration / upCount // 只用成功响应数量
	}
	total := upCount + downCount
	uptime := 100.0
	if total > 0 {
		uptime = float64(upCount) / float64(total) * 100
	}

	status := model.StatusUp
	if len(heartbeats) > 0 {
		status = heartbeats[0].Status // 使用最新一条的状态
	}

	return ChartDataPoint{
		Time:     now.Format(time.RFC3339),
		Duration: avgDuration,
		Status:   int(status),
		Uptime:   uptime,
		IsLive:   true,
	}
}

// getCurrentSlotPoint 获取当前时间段的实时数据点
// 合并已聚合的小时数据和当前小时的原始数据
func getCurrentSlotPoint(monitorID uint, currentHour time.Time, now time.Time, slotHours int) ChartDataPoint {
	// 计算当前时间段的开始时间
	// 例如：如果slotHours=6，当前是14:30，则当前时段是12:00-18:00
	hourOfDay := currentHour.Hour()
	slotIndex := hourOfDay / slotHours
	slotStart := time.Date(currentHour.Year(), currentHour.Month(), currentHour.Day(),
		slotIndex*slotHours, 0, 0, 0, currentHour.Location())

	// 如果slotStart是未来，往前推一个时段
	if slotStart.After(currentHour) {
		slotStart = slotStart.Add(-time.Duration(slotHours) * time.Hour)
	}

	// 1. 获取这个时段内已聚合的小时数据
	var hourlyData []model.HeartbeatHourly
	DB.Where("monitor_id = ? AND hour >= ? AND hour < ?", monitorID, slotStart, currentHour).
		Find(&hourlyData)

	var totalDuration int
	var totalUpCount, totalDownCount, dataCount int

	for _, h := range hourlyData {
		totalDuration += h.AvgDuration
		totalUpCount += h.UpCount
		totalDownCount += h.DownCount
		dataCount++
	}

	// 2. 获取当前小时的原始数据
	var heartbeats []model.Heartbeat
	DB.Where("monitor_id = ? AND time >= ?", monitorID, currentHour).
		Find(&heartbeats)

	var currentHourDuration int
	var currentUpCount, currentDownCount int
	for _, h := range heartbeats {
		// 只统计成功响应的延迟
		if h.Status == model.StatusUp {
			currentHourDuration += h.Duration
			currentUpCount++
		} else if h.Status == model.StatusDown {
			currentDownCount++
		}
	}

	// 合并数据
	if currentUpCount > 0 {
		totalDuration += currentHourDuration / currentUpCount // 只用成功响应数量
		totalUpCount += currentUpCount
		totalDownCount += currentDownCount
		dataCount++
	} else if len(heartbeats) > 0 {
		// 没有成功响应但有失败响应，也要计入downCount
		totalDownCount += currentDownCount
	}

	if dataCount == 0 {
		return ChartDataPoint{
			Time:     now.Format(time.RFC3339),
			Duration: 0,
			Status:   -1,
			Uptime:   100,
			IsLive:   true,
		}
	}

	avgDuration := totalDuration / dataCount
	total := totalUpCount + totalDownCount
	uptime := 100.0
	if total > 0 {
		uptime = float64(totalUpCount) / float64(total) * 100
	}

	status := model.StatusUp
	if uptime < 50 {
		status = model.StatusDown
	}
	// 如果有当前小时数据，使用最新状态
	if len(heartbeats) > 0 {
		status = heartbeats[len(heartbeats)-1].Status
	}

	return ChartDataPoint{
		Time:     now.Format(time.RFC3339),
		Duration: avgDuration,
		Status:   int(status),
		Uptime:   uptime,
		IsLive:   true,
	}
}
