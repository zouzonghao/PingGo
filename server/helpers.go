package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"ping-go/db"
	"ping-go/model"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/zishang520/socket.io/socket"
)

// serveStaticFile 提供静态文件服务
func (s *Server) serveStaticFileHelper(c *gin.Context, filename string) {
	if s.staticFS != nil {
		f, err := s.staticFS.Open(filename)
		if err != nil {
			c.String(http.StatusNotFound, filename+" not found")
			return
		}
		defer f.Close()
		content, err := io.ReadAll(f)
		if err != nil {
			c.String(http.StatusInternalServerError, "failed to read "+filename)
			return
		}

		contentType := "text/html; charset=utf-8"
		if strings.HasSuffix(filename, ".js") {
			contentType = "application/javascript"
		} else if strings.HasSuffix(filename, ".css") {
			contentType = "text/css"
		} else if strings.HasSuffix(filename, ".avif") {
			contentType = "image/avif"
		} else if strings.HasSuffix(filename, ".ico") {
			contentType = "image/x-icon"
		} else if strings.HasSuffix(filename, ".png") {
			contentType = "image/png"
		} else if strings.HasSuffix(filename, ".svg") {
			contentType = "image/svg+xml"
		}

		c.Data(http.StatusOK, contentType, content)
	} else {
		c.String(http.StatusOK, "Frontend not loaded")
	}
}

// getMonitorsAPI REST API 处理器
func (s *Server) getMonitorsAPI(c *gin.Context) {
	var monitors []model.Monitor
	if err := db.DB.Find(&monitors).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, monitors)
}

// createMonitorAPI REST API 处理器
func (s *Server) createMonitorAPI(c *gin.Context) {
	var m model.Monitor
	if err := c.ShouldBindJSON(&m); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if m.Interval < 20 {
		m.Interval = 20
	}

	if err := db.DB.Create(&m).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	s.monitorService.StartMonitor(&m)
	c.JSON(http.StatusCreated, m)
}

// deleteMonitorAPI REST API 处理器
func (s *Server) deleteMonitorAPI(c *gin.Context) {
	idStr := c.Param("id")
	var id uint64
	fmt.Sscanf(idStr, "%d", &id)

	if err := db.DB.Delete(&model.Monitor{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	s.monitorService.StopMonitor(uint(id))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// broadcastMonitorList 广播监控列表给所有客户端
func (s *Server) broadcastMonitorList() {
	var monitors []model.Monitor
	db.DB.Find(&monitors)

	publicData := make(map[uint]map[string]any)
	adminData := make(map[uint]map[string]any)

	for _, m := range monitors {
		data := make(map[string]any)
		data["id"] = m.ID
		data["name"] = m.Name
		data["type"] = m.Type
		data["interval"] = m.Interval
		data["active"] = m.Active
		data["status"] = m.Status
		data["msg"] = m.Message
		data["last_check"] = m.LastCheck
		data["recentResults"] = s.getRecentResults(m.ID)

		pData := make(map[string]any)
		for k, v := range data {
			pData[k] = v
		}
		publicData[m.ID] = pData

		aData := make(map[string]any)
		for k, v := range data {
			aData[k] = v
		}
		aData["url"] = m.URL
		adminData[m.ID] = aData
	}

	s.socketServer.To("public").Emit("monitorList", publicData)
	s.socketServer.To("admin").Emit("adminMonitorList", adminData)
}

// sendMonitorList 发送监控列表给单个客户端
func (s *Server) sendMonitorList(client *socket.Socket) {
	var monitors []model.Monitor
	db.DB.Find(&monitors)
	monitorData := make(map[uint]map[string]any)

	isAuth := false
	if val, ok := socketAuth.Load(client.Id()); ok {
		if data, ok := val.(map[string]any); ok {
			if a, ok := data["authenticated"].(bool); ok && a {
				isAuth = true
			}
		}
	}

	for _, m := range monitors {
		data := make(map[string]any)
		data["id"] = m.ID
		data["name"] = m.Name
		if isAuth {
			data["url"] = m.URL
		}
		data["type"] = m.Type
		data["interval"] = m.Interval
		data["active"] = m.Active
		data["status"] = m.Status
		data["msg"] = m.Message
		data["last_check"] = m.LastCheck
		data["recentResults"] = s.getRecentResults(m.ID)
		monitorData[m.ID] = data
	}

	if isAuth {
		client.Emit("adminMonitorList", monitorData)
	} else {
		client.Emit("monitorList", monitorData)
	}
}

// getRecentResults 获取最近的30条监控结果
func (s *Server) getRecentResults(monitorID uint) []int {
	var statuses []int
	db.DB.Model(&model.Heartbeat{}).
		Where("monitor_id = ?", monitorID).
		Order("time desc").
		Limit(30).
		Pluck("status", &statuses)

	for len(statuses) < 30 {
		statuses = append(statuses, -1)
	}

	for i, j := 0, len(statuses)-1; i < j; i, j = i+1, j-1 {
		statuses[i], statuses[j] = statuses[j], statuses[i]
	}
	return statuses
}

// getMonitorStats 获取监控统计数据
func (s *Server) getMonitorStats(monitorID uint) map[string]any {
	stats := make(map[string]any)
	stats["uptime1h"] = db.GetUptimeStats(monitorID, 1*time.Hour)
	stats["uptime24h"] = db.GetUptimeStats(monitorID, 24*time.Hour)
	stats["uptime7d"] = db.GetUptimeStats(monitorID, 7*24*time.Hour)
	stats["uptime30d"] = db.GetUptimeStats(monitorID, 30*24*time.Hour)
	stats["avgResponse24h"] = db.GetAvgResponseTime(monitorID, 24*time.Hour)
	return stats
}

// sanitizeFormData 验证 form_data 是否为有效的 JSON 数组格式
func sanitizeFormData(s string) string {
	if s == "" {
		return ""
	}
	var arr []interface{}
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return ""
	}
	return s
}
