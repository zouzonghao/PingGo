package server

import (
	"fmt"
	"ping-go/db"
	"ping-go/model"
	"ping-go/pkg/logger"
	"time"

	"github.com/zishang520/socket.io/socket"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// setupAuthHandlers 设置认证相关的 Socket.IO 事件处理器
func (s *Server) setupAuthHandlers(client *socket.Socket) {
	// Handle "checkSetup"
	client.On("checkSetup", func(args ...any) {
		logger.Debug("checkSetup called", zap.String("client", string(client.Id())))
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
			logger.Warn("setup: missing arguments", zap.String("client", string(client.Id())))
			return
		}
		data, ok := args[0].(map[string]any)
		if !ok {
			logger.Warn("setup: invalid data format", zap.String("client", string(client.Id())))
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
				// Create persistent session
				token := generateToken()
				sess := model.Session{
					Token:     token,
					UserID:    user.ID,
					ExpiresAt: time.Now().Add(24 * time.Hour), // 24h expiration
				}
				if err := db.DB.Create(&sess).Error; err != nil {
					client.Emit("error", map[string]any{"msg": "Failed to create session"})
					return
				}

				// Mark as authenticated in socket data
				socketAuth.Store(client.Id(), map[string]any{
					"authenticated": true,
					"userID":        user.ID,
					"token":         token,
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

		var sess model.Session
		err := db.DB.First(&sess, "token = ?", token).Error
		exists := err == nil

		if exists && time.Now().Before(sess.ExpiresAt) {
			socketAuth.Store(client.Id(), map[string]any{
				"authenticated": true,
				"userID":        sess.UserID,
				"token":         token,
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

	// Handle "logout"
	client.On("logout", func(args ...any) {
		if val, ok := socketAuth.Load(client.Id()); ok {
			if data, ok := val.(map[string]any); ok {
				if token, ok := data["token"].(string); ok {
					// Delete session from DB
					db.DB.Delete(&model.Session{}, "token = ?", token)
				}
			}
		}
		socketAuth.Delete(client.Id())
		if len(args) > 0 {
			ack := args[0].(func([]any, error))
			ack([]any{map[string]any{
				"ok": true,
			}}, nil)
		}
	})
}

// requireAuth 创建一个需要认证的事件处理器包装器
func requireAuth(client *socket.Socket, eventName string, handler func(args ...any)) {
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
						var sess model.Session
						if err := db.DB.First(&sess, "token = ?", token).Error; err == nil {
							if time.Now().Before(sess.ExpiresAt) {
								socketAuth.Store(client.Id(), map[string]any{
									"authenticated": true,
									"userID":        sess.UserID,
									"token":         token,
								})
								client.Join("admin")
								handler(args...)
								return
							}
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
