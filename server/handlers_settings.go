package server

import (
	"fmt"
	"ping-go/db"
	"ping-go/model"

	"github.com/zishang520/socket.io/socket"
)

// setupSettingsHandlers 设置系统设置相关的 Socket.IO 事件处理器
func (s *Server) setupSettingsHandlers(client *socket.Socket) {
	// Handle "getSettings"
	requireAuth(client, "getSettings", func(args ...any) {
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
	requireAuth(client, "setSettings", func(args ...any) {
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
}
