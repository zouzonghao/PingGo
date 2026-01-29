package server

import (
	"encoding/json"
	"fmt"
	"ping-go/db"
	"ping-go/model"
	"ping-go/notification"

	"github.com/zishang520/socket.io/socket"
)

// setupNotificationHandlers 设置通知相关的 Socket.IO 事件处理器
func (s *Server) setupNotificationHandlers(client *socket.Socket) {
	// Handle "getNotificationList"
	requireAuth(client, "getNotificationList", func(args ...any) {
		var notifications []model.Notification
		db.DB.Find(&notifications)
		client.Emit("notificationList", notifications)
	})

	// Handle "addNotification"
	requireAuth(client, "addNotification", func(args ...any) {
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
	requireAuth(client, "editNotification", func(args ...any) {
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
		idFloat, err := getFloat64(idVal)
		if err != nil {
			return
		}
		id := uint(idFloat)

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
	requireAuth(client, "deleteNotification", func(args ...any) {
		if len(args) < 1 {
			return
		}
		id, err := getArgAsUint(args, 0)
		if err != nil {
			return
		}
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
	requireAuth(client, "toggleNotification", func(args ...any) {
		if len(args) < 1 {
			return
		}
		id, err := getArgAsUint(args, 0)
		if err != nil {
			return
		}

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
	requireAuth(client, "testNotification", func(args ...any) {
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
}
