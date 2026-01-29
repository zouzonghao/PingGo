package server

import (
	"ping-go/db"
	"ping-go/model"

	"github.com/zishang520/socket.io/socket"
)

// setupHeartbeatHandlers 设置心跳数据相关的 Socket.IO 事件处理器
func (s *Server) setupHeartbeatHandlers(client *socket.Socket) {
	// Handle "getHeartbeatList"
	client.On("getHeartbeatList", func(args ...any) {
		if len(args) < 1 {
			return
		}
		monitorID, err := getArgAsUint(args, 0)
		if err != nil {
			return
		}

		var heartbeats []model.Heartbeat
		db.DB.Where("monitor_id = ?", monitorID).Order("time desc").Limit(30).Find(&heartbeats)

		// Format for frontend
		results := make([]map[string]any, 0)
		for _, h := range heartbeats {
			results = append(results, map[string]any{
				"monitorID": h.MonitorID,
				"status":    h.Status,
				"msg":       h.Message,
				"time":      h.Time.Format("2006-01-02 15:04:05"),
				"duration":  h.Duration,
			})
		}
		client.Emit("heartbeatList", monitorID, results)
	})

	// Handle "getHeartbeatListWithRange" - 支持时间范围智能查询
	// 根据时间范围自动选择数据源：24h内用原始数据，7天内用小时聚合，更长用日聚合
	client.On("getHeartbeatListWithRange", func(args ...any) {
		if len(args) < 2 {
			return
		}
		monitorID, err := getArgAsUint(args, 0)
		if err != nil {
			return
		}
		hoursFloat, err := getArgAsFloat64(args, 1)
		if err != nil {
			return
		}
		hours := int(hoursFloat)

		// 使用智能查询层
		results, dataType := db.GetHeartbeatsWithTimeRange(monitorID, hours)

		// 返回结果和数据类型（让前端知道是原始/小时/日数据）
		client.Emit("heartbeatListWithRange", monitorID, map[string]any{
			"data":     results,
			"dataType": dataType,
			"hours":    hours,
		})
	})

	// Handle "getMonitorStats"
	client.On("getMonitorStats", func(args ...any) {
		if len(args) < 1 {
			return
		}
		monitorID, err := getArgAsUint(args, 0)
		if err != nil {
			return
		}
		stats := s.getMonitorStats(monitorID)
		client.Emit("monitorStats", monitorID, stats)
	})

	// Handle "getChartData" - 获取图表数据
	// 支持 "24h"（24个点）和 "7d"（28个点）两种视图
	// 使用降采样的小时聚合数据，最近一个点从原始数据获取
	client.On("getChartData", func(args ...any) {
		if len(args) < 2 {
			return
		}
		monitorID, err := getArgAsUint(args, 0)
		if err != nil {
			return
		}
		view, _ := args[1].(string) // "24h" 或 "7d"

		// 获取图表数据
		chartData := db.GetChartData(monitorID, view)

		// 返回给客户端
		client.Emit("chartData", monitorID, map[string]any{
			"view": view,
			"data": chartData,
		})
	})

	// Handle "clearEvents" - 清理所有心跳数据（包括聚合数据）
	client.On("clearEvents", func(args ...any) {
		if len(args) < 1 {
			return
		}
		monitorID, err := getArgAsUint(args, 0)
		if err != nil {
			return
		}

		// 清理原始数据
		db.DB.Where("monitor_id = ?", monitorID).Delete(&model.Heartbeat{})
		// 清理小时聚合数据
		db.DB.Where("monitor_id = ?", monitorID).Delete(&model.HeartbeatHourly{})
		// 清理日聚合数据
		db.DB.Where("monitor_id = ?", monitorID).Delete(&model.HeartbeatDaily{})

		if len(args) > 1 {
			ack := args[1].(func([]any, error))
			ack([]any{map[string]any{
				"ok":  true,
				"msg": "All events and aggregated data cleared",
			}}, nil)
		}
	})
}
