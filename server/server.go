package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"ping-go/db"
	"ping-go/model"
	"ping-go/monitor"
	"ping-go/notification"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/zishang520/socket.io/socket"
	"golang.org/x/crypto/bcrypt"
)

type Session struct {
	UserID    uint
	ExpiresAt time.Time
}

var sessions = struct {
	sync.RWMutex
	data map[string]Session
}{data: make(map[string]Session)}

func startSessionCleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	for range ticker.C {
		sessions.Lock()
		now := time.Now()
		for token, sess := range sessions.data {
			if now.After(sess.ExpiresAt) {
				delete(sessions.data, token)
			}
		}
		sessions.Unlock()
	}
}

var socketAuth = sync.Map{} // key: socketID (string), value: map[string]any

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type Server struct {
	router         *gin.Engine
	socketServer   *socket.Server
	monitorService *monitor.Service
	staticFS       http.FileSystem
}

func NewServer(monitorService *monitor.Service, staticFS http.FileSystem) *Server {
	s := &Server{
		router:         gin.Default(),
		socketServer:   socket.NewServer(nil, nil),
		monitorService: monitorService,
		staticFS:       staticFS,
	}

	// Start session cleanup job
	go startSessionCleanup()

	s.socketServer.On("connection", func(clients ...any) {
		client := clients[0].(*socket.Socket)
		fmt.Println("connected:", client.Id())
		client.Join("public")

		// Handle disconnection to clean up socketAuth
		client.On("disconnect", func(reason ...any) {
			socketAuth.Delete(client.Id())
		})

		// Send Info
		client.Emit("info", map[string]any{
			"version": "1.0.0-go",
		})

		// Handle "checkSetup"
		client.On("checkSetup", func(args ...any) {
			fmt.Println("checkSetup called from", client.Id())
			var count int64
			db.DB.Model(&model.User{}).Count(&count)
			if len(args) > 0 {
				ack := args[0].(func([]any, error))
				ack([]any{map[string]any{
					"needSetup": count == 0,
				}}, nil)
			}
		})

		// Handle "setup"
		client.On("setup", func(args ...any) {
			if len(args) < 1 {
				fmt.Printf("setup: missing arguments from %s\n", client.Id())
				return
			}
			data, ok := args[0].(map[string]any)
			if !ok {
				fmt.Printf("setup: invalid data format from %s\n", client.Id())
				return
			}

			username, ok1 := data["username"].(string)
			password, ok2 := data["password"].(string)
			if !ok1 || !ok2 {
				fmt.Printf("setup: missing username or password from %s\n", client.Id())
				return
			}

			// Check if setup is already done
			var count int64
			db.DB.Model(&model.User{}).Count(&count)
			if count > 0 {
				if len(args) > 1 {
					ack := args[1].(func([]any, error))
					ack([]any{map[string]any{
						"ok":  false,
						"msg": "Setup already completed",
					}}, nil)
				}
				return
			}

			// Validate password match if provided
			if confirmPwd, ok := data["confirmPassword"].(string); ok {
				if password != confirmPwd {
					if len(args) > 1 {
						ack := args[1].(func([]any, error))
						ack([]any{map[string]any{
							"ok":  false,
							"msg": "Passwords do not match",
						}}, nil)
					}
					return
				}
			}

			// Hash password
			hashedPwd, err := bcrypt.GenerateFromPassword([]byte(password), 12)
			if err != nil {
				if len(args) > 1 {
					ack := args[1].(func([]any, error))
					ack([]any{map[string]any{
						"ok":  false,
						"msg": "Failed to hash password",
					}}, nil)
				}
				return
			}

			// Create user
			user := model.User{
				Username: username,
				Password: string(hashedPwd),
			}
			db.DB.Create(&user)

			if len(args) > 1 {
				ack := args[1].(func([]any, error))
				ack([]any{map[string]any{
					"ok":  true,
					"msg": "Setup successfully",
				}}, nil)
			}
		})

		// Handle "login"
		client.On("login", func(args ...any) {
			if len(args) < 1 {
				fmt.Printf("login: missing arguments from %s\n", client.Id())
				return
			}
			data, ok := args[0].(map[string]any)
			if !ok {
				fmt.Printf("login: invalid data format from %s\n", client.Id())
				return
			}
			username, ok1 := data["username"].(string)
			password, ok2 := data["password"].(string)
			if !ok1 || !ok2 {
				fmt.Printf("login: missing username or password from %s\n", client.Id())
				return
			}

			var user model.User
			err := db.DB.Where("username = ?", username).First(&user).Error

			if err == nil {
				// Compare password
				if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err == nil {
					// Create session
					token := generateToken()
					sessions.Lock()
					sessions.data[token] = Session{
						UserID:    user.ID,
						ExpiresAt: time.Now().Add(24 * time.Hour),
					}
					sessions.Unlock()

					// Mark as authenticated in socket data
					socketAuth.Store(client.Id(), map[string]any{
						"authenticated": true,
						"userID":        user.ID,
					})
					client.Join("admin")

					if len(args) > 1 {
						ack := args[1].(func([]any, error))
						ack([]any{map[string]any{
							"ok":    true,
							"token": token,
						}}, nil)
					}
					return
				}
			}

			// Fail
			if len(args) > 1 {
				ack := args[1].(func([]any, error))
				ack([]any{map[string]any{
					"ok":  false,
					"msg": "Invalid username or password",
				}}, nil)
			}
		})

		// Handle "auth" for token-based session recovery
		client.On("auth", func(args ...any) {
			if len(args) < 1 {
				return
			}
			data, ok := args[0].(map[string]any)
			if !ok {
				return
			}
			token, _ := data["token"].(string)
			if token == "" {
				return
			}

			sessions.RLock()
			sess, exists := sessions.data[token]
			sessions.RUnlock()

			if exists && time.Now().Before(sess.ExpiresAt) {
				socketAuth.Store(client.Id(), map[string]any{
					"authenticated": true,
					"userID":        sess.UserID,
				})
				client.Join("admin")
				if len(args) > 1 {
					ack := args[1].(func([]any, error))
					ack([]any{map[string]any{
						"ok": true,
					}}, nil)
				}
			} else {
				if len(args) > 1 {
					ack := args[1].(func([]any, error))
					ack([]any{map[string]any{
						"ok":  false,
						"msg": "Invalid or expired token",
					}}, nil)
				}
			}
		})

		// Define protected events helper
		requireAuth := func(eventName string, handler func(args ...any)) {
			client.On(eventName, func(args ...any) {
				var authed bool
				if val, ok := socketAuth.Load(client.Id()); ok {
					if data, ok := val.(map[string]any); ok {
						if a, ok := data["authenticated"].(bool); ok && a {
							authed = true
						}
					}
				}

				if !authed {
					// Try to authenticate via token if provided in the first arg
					if len(args) > 0 {
						if data, ok := args[0].(map[string]any); ok {
							if token, ok := data["token"].(string); ok {
								sessions.RLock()
								sess, exists := sessions.data[token]
								sessions.RUnlock()
								if exists && time.Now().Before(sess.ExpiresAt) {
									socketAuth.Store(client.Id(), map[string]any{
										"authenticated": true,
										"userID":        sess.UserID,
									})
									client.Join("admin")
									handler(args...)
									return
								}
							}
						}
					}

					client.Emit("error", map[string]any{
						"code": 401,
						"msg":  "Unauthorized",
					})
					return
				}
				handler(args...)
			})
		}

		// Handle "logout"
		client.On("logout", func(args ...any) {
			socketAuth.Delete(client.Id())
			if len(args) > 0 {
				ack := args[0].(func([]any, error))
				ack([]any{map[string]any{
					"ok": true,
				}}, nil)
			}
		})

		// Handle "getNotificationList"
		requireAuth("getNotificationList", func(args ...any) {
			var notifications []model.Notification
			db.DB.Find(&notifications)
			client.Emit("notificationList", notifications)
		})

		// Handle "addNotification"
		requireAuth("addNotification", func(args ...any) {
			if len(args) < 1 {
				fmt.Printf("addNotification: missing arguments from %s\n", client.Id())
				return
			}
			data, ok := args[0].(map[string]any)
			if !ok {
				fmt.Printf("addNotification: invalid data format from %s\n", client.Id())
				return
			}

			name, _ := data["name"].(string)
			ntype, _ := data["type"].(string)

			configBytes, _ := json.Marshal(data)

			n := model.Notification{
				Name:   name,
				Type:   ntype,
				Config: string(configBytes),
				Active: true,
			}
			db.DB.Create(&n)

			if len(args) > 1 {
				ack := args[1].(func([]any, error))
				ack([]any{map[string]any{
					"ok":  true,
					"msg": "Notification added",
					"id":  n.ID,
				}}, nil)
			}

			// Broadcast updated list
			var notifications []model.Notification
			db.DB.Find(&notifications)
			s.socketServer.To("public").Emit("notificationList", notifications)
		})

		// Handle "editNotification"
		requireAuth("editNotification", func(args ...any) {
			if len(args) < 1 {
				return
			}
			data, ok := args[0].(map[string]any)
			if !ok {
				return
			}

			idVal, ok := data["id"]
			if !ok {
				return
			}
			id := uint(idVal.(float64))

			var n model.Notification
			if err := db.DB.First(&n, id).Error; err != nil {
				if len(args) > 1 {
					ack := args[1].(func([]any, error))
					ack([]any{map[string]any{
						"ok":  false,
						"msg": "Notification not found",
					}}, nil)
				}
				return
			}

			name, _ := data["name"].(string)
			ntype, _ := data["type"].(string)

			// Remove the id from data to avoid it being stored in config if desired,
			// or just marshal the whole thing as config.
			configBytes, _ := json.Marshal(data)

			n.Name = name
			n.Type = ntype
			n.Config = string(configBytes)
			db.DB.Save(&n)

			if len(args) > 1 {
				ack := args[1].(func([]any, error))
				ack([]any{map[string]any{
					"ok":  true,
					"msg": "Notification updated",
				}}, nil)
			}

			// Broadcast updated list
			var notifications []model.Notification
			db.DB.Find(&notifications)
			s.socketServer.To("public").Emit("notificationList", notifications)
		})

		// Handle "deleteNotification"
		requireAuth("deleteNotification", func(args ...any) {
			if len(args) < 1 {
				return
			}
			id := uint(args[0].(float64))
			db.DB.Delete(&model.Notification{}, id)

			if len(args) > 1 {
				ack := args[1].(func([]any, error))
				ack([]any{map[string]any{
					"ok":  true,
					"msg": "Deleted successfully",
				}}, nil)
			}

			// Broadcast updated list
			var notifications []model.Notification
			db.DB.Find(&notifications)
			s.socketServer.To("public").Emit("notificationList", notifications)
		})

		// Handle "toggleNotification"
		requireAuth("toggleNotification", func(args ...any) {
			if len(args) < 1 {
				return
			}
			id := uint(args[0].(float64))

			var n model.Notification
			if err := db.DB.First(&n, id).Error; err != nil {
				return
			}

			n.Active = !n.Active
			db.DB.Save(&n)

			// Broadcast updated list
			var notifications []model.Notification
			db.DB.Find(&notifications)
			s.socketServer.To("public").Emit("notificationList", notifications)
		})

		// Handle "testNotification"
		requireAuth("testNotification", func(args ...any) {
			if len(args) < 1 {
				fmt.Printf("testNotification: missing arguments from %s\n", client.Id())
				return
			}
			data, ok := args[0].(map[string]any)
			if !ok {
				fmt.Printf("testNotification: invalid data format from %s\n", client.Id())
				return
			}

			// For now, only handle email via Resend
			if t, ok := data["type"].(string); ok && t == "email" {
				// Try to get recipient from data
				recipient, _ := data["resendRecipientEmail"].(string)
				if recipient == "" {
					recipient, _ = data["recipientEmail"].(string) // fallback
				}

				if recipient != "" {
					err := notification.SendEmail([]string{recipient}, "Test Notification", "This is a test notification from ping-go.")
					if len(args) > 1 {
						ack := args[1].(func([]any, error))
						if err == nil {
							ack([]any{map[string]any{
								"ok":  true,
								"msg": "Test email sent",
							}}, nil)
						} else {
							ack([]any{map[string]any{
								"ok":  false,
								"msg": err.Error(),
							}}, nil)
						}
					}
					return
				}
			}

			if len(args) > 1 {
				ack := args[1].(func([]any, error))
				ack([]any{map[string]any{
					"ok":  false,
					"msg": "Unsupported notification type or missing recipient",
				}}, nil)
			}
		})

		// Handle "getSettings"
		requireAuth("getSettings", func(args ...any) {
			var settings []model.Setting
			db.DB.Find(&settings)
			settingsMap := make(map[string]any)
			for _, setting := range settings {
				settingsMap[setting.Key] = setting.Value
			}
			// Add some default settings if missing
			if _, ok := settingsMap["siteName"]; !ok {
				settingsMap["siteName"] = "ping-go"
			}
			client.Emit("settings", settingsMap)
		})

		// Handle "setSettings"
		requireAuth("setSettings", func(args ...any) {
			if len(args) < 1 {
				return
			}
			settingsMap := args[0].(map[string]any)
			for k, v := range settingsMap {
				var setting model.Setting
				db.DB.Where("key = ?", k).First(&setting)
				setting.Key = k
				setting.Value = fmt.Sprintf("%v", v)
				db.DB.Save(&setting)
			}
			if len(args) > 1 {
				ack := args[1].(func([]any, error))
				ack([]any{map[string]any{
					"ok":  true,
					"msg": "Settings saved",
				}}, nil)
			}
		})

		// Handle "getMonitorList"
		client.On("getMonitorList", func(args ...any) {
			s.sendMonitorList(client)
		})

		// Handle "getMonitor"
		requireAuth("getMonitor", func(args ...any) {
			if len(args) < 1 {
				return
			}
			id := uint(args[0].(float64))
			var m model.Monitor
			if err := db.DB.First(&m, id).Error; err == nil {
				data := make(map[string]any)
				data["id"] = m.ID
				data["name"] = m.Name
				data["url"] = m.URL
				data["type"] = m.Type
				data["interval"] = m.Interval
				data["active"] = m.Active
				data["status"] = m.Status
				data["msg"] = m.Message
				data["last_check"] = m.LastCheck
				data["recentResults"] = s.getRecentResults(m.ID)

				// Return to the authenticated socket
				client.Emit("monitor", data)
			}
		})
		// Handle "getHeartbeatList"
		client.On("getHeartbeatList", func(args ...any) {
			if len(args) < 1 {
				return
			}
			monitorID := uint(args[0].(float64))

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
			monitorID := uint(args[0].(float64))
			hours := int(args[1].(float64)) // 查询时间范围（小时）

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
			monitorID := uint(args[0].(float64))
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
			monitorID := uint(args[0].(float64))
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
			monitorID := uint(args[0].(float64))

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

		// Handle "add"
		requireAuth("add", func(args ...any) {
			if len(args) < 1 {
				return
			}

			data, ok := args[0].(map[string]any)
			if !ok {
				return
			}

			// Extract optional fields with defaults
			method, _ := data["method"].(string)
			if method == "" {
				method = "GET"
			}
			body, _ := data["body"].(string)
			headers, _ := data["headers"].(string)

			timeout := 10
			if t, ok := data["timeout"].(float64); ok {
				timeout = int(t)
			}

			expectedStatus := 0
			if s, ok := data["expected_status"].(float64); ok {
				expectedStatus = int(s)
			}

			responseRegex, _ := data["response_regex"].(string)

			followRedirects := true
			if fr, ok := data["follow_redirects"].(bool); ok {
				followRedirects = fr
			}

			m := model.Monitor{
				Name:            data["name"].(string),
				URL:             data["url"].(string),
				Type:            model.MonitorType(data["type"].(string)),
				Interval:        int(data["interval"].(float64)),
				Method:          method,
				Body:            body,
				Headers:         headers,
				Timeout:         timeout,
				ExpectedStatus:  expectedStatus,
				ResponseRegex:   responseRegex,
				FollowRedirects: followRedirects,
				Status:          model.StatusPending,
				Active:          1,
			}

			if m.Interval < 20 {
				m.Interval = 20
			}

			// Check for duplicate name
			var count int64
			db.DB.Model(&model.Monitor{}).Where("name = ?", data["name"].(string)).Count(&count)
			if count > 0 {
				client.Emit("notification", map[string]any{
					"message": "监控项名称已存在，请使用唯一名称",
					"type":    "error",
				})
				return
			}

			if err := db.DB.Create(&m).Error; err != nil {
				client.Emit("notification", map[string]any{
					"message": "Failed to add monitor: " + err.Error(),
					"type":    "error",
				})
				return
			}

			s.monitorService.StartMonitor(&m)

			// Ack with success - find if there is a callback in args
			for _, arg := range args {
				if ack, ok := arg.(func([]any, error)); ok {
					ack([]any{map[string]any{
						"ok":        true,
						"msg":       "Added successfully",
						"monitorID": m.ID,
					}}, nil)
					break
				}
			}

			// Broadcast updated list
			s.broadcastMonitorList()
		})

		// Handle "edit"
		requireAuth("edit", func(args ...any) {
			if len(args) < 1 {
				return
			}

			data, ok := args[0].(map[string]any)
			if !ok {
				return
			}

			id := uint(data["id"].(float64))
			var m model.Monitor
			if err := db.DB.First(&m, id).Error; err != nil {
				return
			}

			// Store old active state to detect change
			oldActive := m.Active

			m.Name = data["name"].(string)
			m.URL = data["url"].(string)
			m.Type = model.MonitorType(data["type"].(string))
			m.Interval = int(data["interval"].(float64))

			// Handle active field for pause/resume functionality
			if active, ok := data["active"].(float64); ok {
				m.Active = int(active)
			}

			if method, ok := data["method"].(string); ok && method != "" {
				m.Method = method
			} else {
				m.Method = "GET"
			}
			m.Body, _ = data["body"].(string)
			m.Headers, _ = data["headers"].(string)

			if t, ok := data["timeout"].(float64); ok {
				m.Timeout = int(t)
			} else {
				m.Timeout = 10
			}

			if s, ok := data["expected_status"].(float64); ok {
				m.ExpectedStatus = int(s)
			} else {
				m.ExpectedStatus = 0
			}

			m.ResponseRegex, _ = data["response_regex"].(string)

			if fr, ok := data["follow_redirects"].(bool); ok {
				m.FollowRedirects = fr
			} else {
				m.FollowRedirects = true
			}

			if m.Interval < 20 {
				m.Interval = 20
			}

			// Check for duplicate name (excluding self)
			if m.Name != data["name"].(string) {
				var count int64
				db.DB.Model(&model.Monitor{}).Where("name = ? AND id != ?", data["name"].(string), id).Count(&count)
				if count > 0 {
					client.Emit("notification", map[string]any{
						"message": "监控项名称已存在，请使用唯一名称",
						"type":    "error",
					})
					return
				}
			}

			if err := db.DB.Save(&m).Error; err != nil {
				client.Emit("notification", map[string]any{
					"message": "Failed to edit monitor: " + err.Error(),
					"type":    "error",
				})
				return
			}

			// Handle monitor start/stop based on active state change
			if oldActive != m.Active {
				if m.Active == 0 {
					// Pause: just stop the monitor
					s.monitorService.StopMonitor(m.ID)
				} else {
					// Resume: start the monitor
					s.monitorService.StartMonitor(&m)
				}
			} else if m.Active == 1 {
				// If still active, restart to apply changes
				s.monitorService.StopMonitor(m.ID)
				s.monitorService.StartMonitor(&m)
			}

			// Ack
			for _, arg := range args {
				if ack, ok := arg.(func([]any, error)); ok {
					ack([]any{map[string]any{
						"ok":        true,
						"msg":       "Saved successfully",
						"monitorID": m.ID,
					}}, nil)
					break
				}
			}

			// Broadcast updated list
			s.broadcastMonitorList()
		})

		// Handle "deleteMonitor"
		requireAuth("deleteMonitor", func(args ...any) {
			if len(args) < 1 {
				return
			}
			id := uint(args[0].(float64))

			if err := db.DB.Delete(&model.Monitor{}, id).Error; err != nil {
				return
			}

			s.monitorService.StopMonitor(id)

			// 清理所有相关的心跳数据
			db.DB.Where("monitor_id = ?", id).Delete(&model.Heartbeat{})
			db.DB.Where("monitor_id = ?", id).Delete(&model.HeartbeatHourly{})
			db.DB.Where("monitor_id = ?", id).Delete(&model.HeartbeatDaily{})

			// Ack
			for _, arg := range args {
				if ack, ok := arg.(func([]any, error)); ok {
					ack([]any{map[string]any{
						"ok":  true,
						"msg": "Deleted successfully",
					}}, nil)
					break
				}
			}

			// Broadcast updated list
			s.broadcastMonitorList()
		})

		client.On("disconnect", func(reason ...any) {
			fmt.Println("closed", client.Id())
			socketAuth.Delete(client.Id())
		})
	})

	// Bind Monitor Status Change to Socket Emit
	// Bind Monitor Heartbeat to Socket Emit
	s.monitorService.OnHeartbeat = func(h *model.Heartbeat) {
		// Emit heartbeat event which frontend expects
		heartbeat := map[string]any{
			"monitorID": h.MonitorID,
			"status":    h.Status,
			"msg":       h.Message,
			"time":      h.Time.Format("2006-01-02 15:04:05"),
			"duration":  h.Duration,
		}
		s.socketServer.To("public").Emit("heartbeat", heartbeat)
	}

	// CORS config
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowOriginFunc = func(origin string) bool {
		// In debug mode or if origin is localhost/127.0.0.1, allow it
		if strings.Contains(origin, "localhost") || strings.Contains(origin, "127.0.0.1") {
			return true
		}
		// You can add more production domains here
		return false
	}
	corsConfig.AllowCredentials = true
	corsConfig.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type", "Authorization"}
	s.router.Use(cors.New(corsConfig))

	s.registerRoutes()
	return s
}

func (s *Server) Router() *gin.Engine {
	return s.router
}

func (s *Server) registerRoutes() {
	// Public API - DISABLED for security (exposes sensitive URLs)
	// api := s.router.Group("/api")
	// {
	// 	api.GET("/monitors", s.getMonitors)
	// 	api.POST("/monitors", s.createMonitor)
	// 	api.DELETE("/monitors/:id", s.deleteMonitor)
	// }

	// Serve index.html as homepage
	s.router.GET("/", func(c *gin.Context) {
		s.serveStaticFile(c, "index.html")
	})

	// Serve dashboard at /dashboard
	s.router.GET("/dashboard", func(c *gin.Context) {
		s.serveStaticFile(c, "admin.html")
	})

	// Socket.IO Handler
	handler := s.socketServer.ServeHandler(nil)
	s.router.GET("/socket.io/*any", gin.WrapH(handler))
	s.router.POST("/socket.io/*any", gin.WrapH(handler))
}

func (s *Server) serveStaticFile(c *gin.Context, filename string) {
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
		if len(filename) > 3 && filename[len(filename)-3:] == ".js" {
			contentType = "application/javascript"
		} else if len(filename) > 4 && filename[len(filename)-4:] == ".css" {
			contentType = "text/css"
		}

		c.Data(http.StatusOK, contentType, content)
	} else {
		c.String(http.StatusOK, "Frontend not loaded")
	}
}

func (s *Server) getMonitors(c *gin.Context) {
	var monitors []model.Monitor
	if err := db.DB.Find(&monitors).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, monitors)
}

func (s *Server) createMonitor(c *gin.Context) {
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

func (s *Server) deleteMonitor(c *gin.Context) {
	idStr := c.Param("id")
	var id uint64
	// simple atoi
	fmt.Sscanf(idStr, "%d", &id)

	if err := db.DB.Delete(&model.Monitor{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	s.monitorService.StopMonitor(uint(id))

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (s *Server) SetStatic(fs http.FileSystem) {
	s.router.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if len(path) >= 4 && path[:4] == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not Found"})
			return
		}

		// Clean path
		cleanPath := path
		if len(cleanPath) > 0 && cleanPath[0] == '/' {
			cleanPath = cleanPath[1:]
		}

		// Try to serve actual file
		f, err := fs.Open(cleanPath)
		if err == nil {
			f.Close()
			// Use our helper to serve with correct content type
			s.serveStaticFile(c, cleanPath)
			return
		}

		// SPA Fallback logic
		if strings.HasPrefix(path, "/dashboard") {
			s.serveStaticFile(c, "admin.html")
		} else {
			s.serveStaticFile(c, "index.html")
		}
	})
}

func (s *Server) broadcastMonitorList() {
	var monitors []model.Monitor
	db.DB.Find(&monitors)

	// Prepare sanitized data (public)
	publicData := make(map[uint]map[string]any)
	// Prepare full data (admin)
	adminData := make(map[uint]map[string]any)

	for _, m := range monitors {
		// Base data
		data := make(map[string]any)
		data["id"] = m.ID
		data["name"] = m.Name
		// URL is excluded from base data
		data["type"] = m.Type
		data["interval"] = m.Interval
		data["active"] = m.Active
		data["status"] = m.Status
		data["msg"] = m.Message
		data["last_check"] = m.LastCheck
		data["recentResults"] = s.getRecentResults(m.ID)

		// Add to public map (copy to ensure safety if we modify later)
		pData := make(map[string]any)
		for k, v := range data {
			pData[k] = v
		}
		publicData[m.ID] = pData

		// Add to admin map (include URL)
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

func (s *Server) sendMonitorList(client *socket.Socket) {
	var monitors []model.Monitor
	db.DB.Find(&monitors)
	monitorData := make(map[uint]map[string]any)
	// Check auth
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

func (s *Server) getRecentResults(monitorID uint) []int {
	var statuses []int
	db.DB.Model(&model.Heartbeat{}).
		Where("monitor_id = ?", monitorID).
		Order("time desc").
		Limit(30).
		Pluck("status", &statuses)

	// 如果不足30条，用-1补齐
	for len(statuses) < 30 {
		statuses = append(statuses, -1)
	}

	// 反转数组，使时间顺序从旧到新
	for i, j := 0, len(statuses)-1; i < j; i, j = i+1, j-1 {
		statuses[i], statuses[j] = statuses[j], statuses[i]
	}
	return statuses
}

func (s *Server) getMonitorStats(monitorID uint) map[string]any {
	stats := make(map[string]any)

	// 使用智能查询层，自动选择最优数据源
	stats["uptime1h"] = db.GetUptimeStats(monitorID, 1*time.Hour)
	stats["uptime24h"] = db.GetUptimeStats(monitorID, 24*time.Hour)
	stats["uptime7d"] = db.GetUptimeStats(monitorID, 7*24*time.Hour)
	stats["uptime30d"] = db.GetUptimeStats(monitorID, 30*24*time.Hour)

	// 平均响应时间
	stats["avgResponse24h"] = db.GetAvgResponseTime(monitorID, 24*time.Hour)

	return stats
}
