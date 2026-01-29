package model

import "time"

// HeartbeatHourly 小时级聚合数据
// 用于存储每小时的汇总统计信息，减少存储空间并提高长周期查询性能
type HeartbeatHourly struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	MonitorID uint      `gorm:"index:idx_hourly_monitor_time" json:"monitorID"`
	Hour      time.Time `gorm:"index:idx_hourly_monitor_time" json:"hour"` // 整点时间 (如 2024-01-29 14:00:00)

	// 状态统计
	UpCount    int `json:"upCount"`    // UP 次数
	DownCount  int `json:"downCount"`  // DOWN 次数
	TotalCount int `json:"totalCount"` // 总检查次数

	// 响应时间统计 (毫秒) - 只统计成功响应
	SumDuration int `json:"sumDuration"` // 成功响应的延迟总和，用于加权平均计算
	AvgDuration int `json:"avgDuration"` // 平均响应时间
	MinDuration int `json:"minDuration"` // 最小响应时间
	MaxDuration int `json:"maxDuration"` // 最大响应时间

	// 可用率 (0-10000 表示 0.00%-100.00%，使用int节省空间)
	Uptime int `json:"uptime"`
}

// HeartbeatDaily 日级聚合数据
// 用于存储每天的汇总统计信息，适用于长期趋势分析和 SLA 计算
type HeartbeatDaily struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	MonitorID uint      `gorm:"index:idx_daily_monitor_time" json:"monitorID"`
	Date      time.Time `gorm:"index:idx_daily_monitor_time" json:"date"` // 日期 (00:00:00)

	// 状态统计
	UpCount    int `json:"upCount"`
	DownCount  int `json:"downCount"`
	TotalCount int `json:"totalCount"`

	// 响应时间统计 (毫秒) - 只统计成功响应
	SumDuration int `json:"sumDuration"` // 成功响应的延迟总和
	AvgDuration int `json:"avgDuration"`
	MinDuration int `json:"minDuration"`
	MaxDuration int `json:"maxDuration"`

	// 可用率 (0-10000 表示 0.00%-100.00%)
	Uptime int `json:"uptime"`
}

// GetUptimePercent 获取可用率百分比 (用于显示)
func (h *HeartbeatHourly) GetUptimePercent() float64 {
	return float64(h.Uptime) / 100.0
}

// GetUptimePercent 获取可用率百分比 (用于显示)
func (h *HeartbeatDaily) GetUptimePercent() float64 {
	return float64(h.Uptime) / 100.0
}
