package server

import (
	"ping-go/db"
	"ping-go/model"

	"github.com/zishang520/socket.io/socket"
)

// setupAddMonitorHandler 设置添加监控项的处理器
func (s *Server) setupAddMonitorHandler(client *socket.Socket) {
	requireAuth(client, "add", func(args ...any) {
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

		name := safeMapGetString(data, "name")
		if name == "" {
			client.Emit("notification", map[string]any{"message": "Name is required", "type": "error"})
			return
		}
		url := safeMapGetString(data, "url")
		mType := safeMapGetString(data, "type")
		intervalFloat, _ := safeMapGetFloat64(data, "interval")
		interval := int(intervalFloat)

		m := model.Monitor{
			Name: name, URL: url, Type: model.MonitorType(mType), Interval: interval,
			Method: method, Body: body, Headers: headers, Timeout: timeout,
			ExpectedStatus: expectedStatus, ResponseRegex: responseRegex,
			FormData: formData, FollowRedirects: followRedirects,
			Status: model.StatusPending, Active: 1,
		}

		if m.Interval < 20 {
			m.Interval = 20
		}

		var count int64
		db.DB.Model(&model.Monitor{}).Where("name = ?", name).Count(&count)
		if count > 0 {
			for _, arg := range args {
				if ack, ok := arg.(func([]any, error)); ok {
					ack([]any{map[string]any{"ok": false, "msg": "监控项名称已存在，请使用唯一名称"}}, nil)
					return
				}
			}
			return
		}

		if err := db.DB.Create(&m).Error; err != nil {
			client.Emit("notification", map[string]any{"message": "Failed to add monitor: " + err.Error(), "type": "error"})
			return
		}

		s.monitorService.StartMonitor(&m)

		for _, arg := range args {
			if ack, ok := arg.(func([]any, error)); ok {
				ack([]any{map[string]any{"ok": true, "msg": "Added successfully", "monitorID": m.ID}}, nil)
				break
			}
		}
		s.broadcastMonitorList()
	})
}

// setupEditMonitorHandler 设置编辑监控项的处理器
func (s *Server) setupEditMonitorHandler(client *socket.Socket) {
	requireAuth(client, "edit", func(args ...any) {
		if len(args) < 1 {
			return
		}
		data, ok := args[0].(map[string]any)
		if !ok {
			return
		}

		idFloat, ok := safeMapGetFloat64(data, "id")
		if !ok {
			return
		}
		id := uint(idFloat)
		var m model.Monitor
		if err := db.DB.First(&m, id).Error; err != nil {
			return
		}

		oldActive := m.Active
		newName := safeMapGetString(data, "name")
		if newName == "" {
			client.Emit("notification", map[string]any{"message": "Name is required", "type": "error"})
			return
		}

		if m.Name != newName {
			var count int64
			db.DB.Model(&model.Monitor{}).Where("name = ? AND id != ?", newName, id).Count(&count)
			if count > 0 {
				for _, arg := range args {
					if ack, ok := arg.(func([]any, error)); ok {
						ack([]any{map[string]any{"ok": false, "msg": "监控项名称已存在，请使用唯一名称"}}, nil)
						return
					}
				}
				return
			}
		}

		m.Name = newName
		m.URL = safeMapGetString(data, "url")
		m.Type = model.MonitorType(safeMapGetString(data, "type"))

		if intervalFloat, ok := safeMapGetFloat64(data, "interval"); ok {
			m.Interval = int(intervalFloat)
		} else {
			m.Interval = 60
		}
		if active, ok := safeMapGetFloat64(data, "active"); ok {
			m.Active = int(active)
		}
		if method := safeMapGetString(data, "method"); method != "" {
			m.Method = method
		} else {
			m.Method = "GET"
		}
		m.Body = safeMapGetString(data, "body")
		m.Headers = safeMapGetString(data, "headers")
		if t, ok := safeMapGetFloat64(data, "timeout"); ok {
			m.Timeout = int(t)
		} else {
			m.Timeout = 10
		}
		if st, ok := safeMapGetFloat64(data, "expected_status"); ok {
			m.ExpectedStatus = int(st)
		} else {
			m.ExpectedStatus = 0
		}
		m.ResponseRegex = convertJSONToRegex(safeMapGetString(data, "response_regex"))
		m.FormData = safeMapGetString(data, "form_data")
		if fr, ok := data["follow_redirects"].(bool); ok {
			m.FollowRedirects = fr
		} else {
			m.FollowRedirects = true
		}
		if m.Interval < 20 {
			m.Interval = 20
		}

		if err := db.DB.Save(&m).Error; err != nil {
			client.Emit("notification", map[string]any{"message": "Failed to edit monitor: " + err.Error(), "type": "error"})
			return
		}

		if oldActive != m.Active {
			if m.Active == 0 {
				s.monitorService.StopMonitor(m.ID)
			} else {
				s.monitorService.StartMonitor(&m)
			}
		} else if m.Active == 1 {
			s.monitorService.StopMonitor(m.ID)
			s.monitorService.StartMonitor(&m)
		}

		for _, arg := range args {
			if ack, ok := arg.(func([]any, error)); ok {
				ack([]any{map[string]any{"ok": true, "msg": "Saved successfully", "monitorID": m.ID}}, nil)
				break
			}
		}
		s.broadcastMonitorList()
	})
}

// setupToggleActiveHandler 设置切换监控项启用状态的处理器
func (s *Server) setupToggleActiveHandler(client *socket.Socket) {
	requireAuth(client, "toggleActive", func(args ...any) {
		if len(args) < 2 {
			return
		}
		idFloat, ok := args[0].(float64)
		if !ok {
			return
		}
		activeFloat, ok := args[1].(float64)
		if !ok {
			return
		}
		id := uint(idFloat)
		newActive := int(activeFloat)

		var m model.Monitor
		if err := db.DB.First(&m, id).Error; err != nil {
			return
		}

		oldActive := m.Active
		m.Active = newActive
		if err := db.DB.Save(&m).Error; err != nil {
			for _, arg := range args {
				if ack, ok := arg.(func([]any, error)); ok {
					ack([]any{map[string]any{"ok": false, "msg": err.Error()}}, nil)
					return
				}
			}
			return
		}

		if oldActive != newActive {
			if newActive == 0 {
				s.monitorService.StopMonitor(m.ID)
			} else {
				s.monitorService.StartMonitor(&m)
			}
		}

		for _, arg := range args {
			if ack, ok := arg.(func([]any, error)); ok {
				ack([]any{map[string]any{"ok": true}}, nil)
				break
			}
		}
		s.broadcastMonitorList()
	})
}

// setupDeleteMonitorHandler 设置删除监控项的处理器
func (s *Server) setupDeleteMonitorHandler(client *socket.Socket) {
	requireAuth(client, "deleteMonitor", func(args ...any) {
		if len(args) < 1 {
			return
		}
		id := uint(args[0].(float64))

		if err := db.DB.Delete(&model.Monitor{}, id).Error; err != nil {
			return
		}

		s.monitorService.StopMonitor(id)
		db.DB.Where("monitor_id = ?", id).Delete(&model.Heartbeat{})
		db.DB.Where("monitor_id = ?", id).Delete(&model.HeartbeatHourly{})
		db.DB.Where("monitor_id = ?", id).Delete(&model.HeartbeatDaily{})

		for _, arg := range args {
			if ack, ok := arg.(func([]any, error)); ok {
				ack([]any{map[string]any{"ok": true, "msg": "Deleted successfully"}}, nil)
				break
			}
		}
		s.broadcastMonitorList()
	})
}
