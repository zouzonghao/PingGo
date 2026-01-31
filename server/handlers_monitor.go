package server

import (
	"encoding/json"
	"fmt"
	"ping-go/db"
	"ping-go/model"
	"ping-go/monitor"
	"regexp"
	"strings"

	"github.com/zishang520/socket.io/socket"
)

// setupMonitorHandlers 设置监控相关的 Socket.IO 事件处理器
func (s *Server) setupMonitorHandlers(client *socket.Socket) {
	// Handle "getMonitorList"
	client.On("getMonitorList", func(args ...any) {
		s.sendMonitorList(client)
	})

	// Handle "getMonitor"
	requireAuth(client, "getMonitor", func(args ...any) {
		if len(args) < 1 {
			return
		}
		id, err := getArgAsUint(args, 0)
		if err != nil {
			return
		}
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
			data["method"] = m.Method
			data["body"] = m.Body
			data["headers"] = m.Headers
			data["timeout"] = m.Timeout
			data["expected_status"] = m.ExpectedStatus

			data["response_regex"] = m.ResponseRegex
			data["form_data"] = m.FormData
			data["follow_redirects"] = m.FollowRedirects
			client.Emit("monitor", data)
		}
	})

	// Handle "exportMonitorConfig"
	requireAuth(client, "exportMonitorConfig", func(args ...any) {
		var monitors []model.Monitor
		if err := db.DB.Find(&monitors).Error; err != nil {
			client.Emit("error", map[string]any{"msg": "Failed to fetch monitors"})
			return
		}
		client.Emit("monitorConfigExport", monitors)
	})

	// Handle "importMonitorConfig"
	s.setupImportMonitorHandler(client)
	// Handle "testMonitor"
	s.setupTestMonitorHandler(client)
	// Handle "add"
	s.setupAddMonitorHandler(client)
	// Handle "edit"
	s.setupEditMonitorHandler(client)
	// Handle "toggleActive"
	s.setupToggleActiveHandler(client)
	// Handle "deleteMonitor"
	s.setupDeleteMonitorHandler(client)
}

func (s *Server) setupImportMonitorHandler(client *socket.Socket) {
	requireAuth(client, "importMonitorConfig", func(args ...any) {
		if len(args) < 1 {
			return
		}
		var monitorsInput []model.Monitor
		jsonData, err := json.Marshal(args[0])
		if err != nil {
			client.Emit("error", map[string]any{"msg": "Invalid data format"})
			return
		}
		if err := json.Unmarshal(jsonData, &monitorsInput); err != nil {
			client.Emit("error", map[string]any{"msg": "Invalid JSON format"})
			return
		}

		importedCount := 0
		skippedCount := 0
		var skippedNames []string

		for _, m := range monitorsInput {
			if m.Name == "" || m.URL == "" {
				continue
			}
			var count int64
			db.DB.Model(&model.Monitor{}).Where("name = ?", m.Name).Count(&count)
			if count > 0 {
				skippedCount++
				skippedNames = append(skippedNames, m.Name)
				continue
			}

			newMonitor := model.Monitor{
				Name: m.Name, URL: m.URL,
				Type: func() model.MonitorType {
					switch m.Type {
					case model.MonitorTypeHTTP, model.MonitorTypePing, model.MonitorTypeTCP, model.MonitorTypeDNS:
						return m.Type
					default:
						return model.MonitorTypeHTTP
					}
				}(),
				Method: m.Method, Body: m.Body, Headers: m.Headers,
				FormData: sanitizeFormData(m.FormData), Timeout: m.Timeout,
				ExpectedStatus: m.ExpectedStatus, ResponseRegex: m.ResponseRegex,
				FollowRedirects: m.FollowRedirects, Interval: m.Interval,
				Active: m.Active, Weight: m.Weight,
			}
			if newMonitor.Interval < 10 {
				newMonitor.Interval = 60
			}
			if newMonitor.Timeout < 1 {
				newMonitor.Timeout = 10
			}
			if newMonitor.Method == "" {
				newMonitor.Method = "GET"
			}

			if err := db.DB.Create(&newMonitor).Error; err == nil {
				importedCount++
				if newMonitor.Active == 1 {
					s.monitorService.StartMonitor(&newMonitor)
				}
			}
		}

		if len(args) > 1 {
			ack := args[1].(func([]any, error))
			ack([]any{map[string]any{
				"ok": true, "imported": importedCount,
				"skipped": skippedCount, "skippedNames": skippedNames,
			}}, nil)
		}
		s.socketServer.To("public").Emit("updateMonitorList")
	})
}

func (s *Server) setupTestMonitorHandler(client *socket.Socket) {
	requireAuth(client, "testMonitor", func(args ...any) {
		if len(args) < 1 {
			return
		}
		data, ok := args[0].(map[string]any)
		if !ok {
			return
		}

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
		if st, ok := data["expected_status"].(float64); ok {
			expectedStatus = int(st)
		}
		responseRegex, _ := data["response_regex"].(string)
		responseRegex = convertJSONToRegex(responseRegex)
		formData, _ := data["form_data"].(string)
		followRedirects := true
		if fr, ok := data["follow_redirects"].(bool); ok {
			followRedirects = fr
		}

		m := model.Monitor{
			URL: safeMapGetString(data, "url"), Type: model.MonitorType(safeMapGetString(data, "type")),
			Method: method, Body: body, Headers: headers, Timeout: timeout,
			ExpectedStatus: expectedStatus, ResponseRegex: responseRegex,
			FormData: formData, FollowRedirects: followRedirects,
		}

		var status int
		var msg string
		switch m.Type {
		case model.MonitorTypeHTTP:
			status, msg = monitor.TestHTTP(m)
		case model.MonitorTypePing:
			st, m2, _ := monitor.CheckPing(m.URL, m.Timeout)
			msg = m2
			if st == model.StatusUp {
				status = 200
			}
		case model.MonitorTypeTCP:
			st, m2, _ := monitor.CheckTCP(m.URL, m.Timeout)
			msg = m2
			if st == model.StatusUp {
				status = 200
			}
		case model.MonitorTypeDNS:
			st, m2 := monitor.CheckDNS(m.URL, m.Timeout)
			msg = m2
			if st == model.StatusUp {
				status = 200
			}
		default:
			msg = "Unknown monitor type"
		}

		if len(msg) > 50000 {
			msg = msg[:50000] + "..."
		}
		if len(args) > 1 {
			ack := args[1].(func([]any, error))
			ack([]any{map[string]any{"ok": true, "status": status, "msg": msg}}, nil)
		}
	})
}

// convertJSONToRegex 将 JSON 格式输入转换为正则表达式
func convertJSONToRegex(responseRegex string) string {
	if responseRegex != "" && json.Valid([]byte(responseRegex)) {
		keyRe := regexp.MustCompile(`"([^"\\]*(?:\\.[^"\\]*)*)"\s*:`)
		matches := keyRe.FindAllStringSubmatch(responseRegex, -1)
		if len(matches) > 0 {
			var builder strings.Builder
			builder.WriteString("(?s)\\{")
			for _, match := range matches {
				builder.WriteString(fmt.Sprintf(".*\"%s\"", match[1]))
			}
			builder.WriteString(".*\\}")
			return builder.String()
		}
	}
	return responseRegex
}
