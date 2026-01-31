package server

import (
	"fmt"
	"net/http"
	"ping-go/db"
	"ping-go/model"
	"ping-go/monitor"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/zishang520/socket.io/socket"
)

// Server 是应用程序的核心服务器结构体
type Server struct {
	router         *gin.Engine
	socketServer   *socket.Server
	monitorService *monitor.Service
	staticFS       http.FileSystem
}

// NewServer 创建并初始化一个新的服务器实例
func NewServer(monitorService *monitor.Service, staticFS http.FileSystem) *Server {
	s := &Server{
		router:         gin.Default(),
		socketServer:   socket.NewServer(nil, nil),
		monitorService: monitorService,
		staticFS:       staticFS,
	}

	// 健康检查端点
	s.router.GET("/health", func(c *gin.Context) {
		health := s.monitorService.HealthCheck()
		sqlDB, err := db.DB.DB()
		if err != nil {
			health["database"] = "error"
		} else if err := sqlDB.Ping(); err != nil {
			health["database"] = "down"
		} else {
			health["database"] = "up"
		}
		c.JSON(http.StatusOK, health)
	})

	// 指标端点
	s.router.GET("/metrics", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "enabled"})
	})

	// 启动会话清理任务
	go startSessionCleanup()

	// 设置 Socket.IO 连接处理
	s.setupSocketHandlers()

	// 绑定监控心跳回调
	s.monitorService.OnHeartbeat = func(h *model.Heartbeat) {
		heartbeat := map[string]any{
			"id":        h.ID,
			"monitorID": h.MonitorID,
			"status":    h.Status,
			"msg":       h.Message,
			"time":      h.Time.Format(time.RFC3339),
			"duration":  h.Duration,
		}
		s.socketServer.To("public").Emit("heartbeat", heartbeat)
	}

	// CORS 配置
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowOriginFunc = func(origin string) bool {
		if strings.Contains(origin, "localhost") || strings.Contains(origin, "127.0.0.1") {
			return true
		}
		return false
	}
	corsConfig.AllowCredentials = true
	corsConfig.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type", "Authorization"}
	s.router.Use(cors.New(corsConfig))

	s.registerRoutes()
	return s
}

// setupSocketHandlers 设置所有 Socket.IO 事件处理器
func (s *Server) setupSocketHandlers() {
	s.socketServer.On("connection", func(clients ...any) {
		client := clients[0].(*socket.Socket)
		client.Join("public")

		// 断开连接时清理认证状态
		client.On("disconnect", func(reason ...any) {
			socketAuth.Delete(client.Id())
		})

		// 发送服务器信息
		client.Emit("info", map[string]any{
			"version": "1.0.0-go",
		})

		// 设置各功能模块的事件处理器
		s.setupAuthHandlers(client)
		s.setupNotificationHandlers(client)
		s.setupSettingsHandlers(client)
		s.setupMonitorHandlers(client)
		s.setupHeartbeatHandlers(client)

		// 断开连接日志
		client.On("disconnect", func(reason ...any) {
			fmt.Println("closed", client.Id())
			socketAuth.Delete(client.Id())
		})
	})
}

// Router 返回 gin.Engine 实例
func (s *Server) Router() *gin.Engine {
	return s.router
}

// registerRoutes 注册 HTTP 路由
func (s *Server) registerRoutes() {
	// 主页
	s.router.GET("/", func(c *gin.Context) {
		s.serveStaticFileGin(c, "index.html")
	})

	// 管理面板
	s.router.GET("/dashboard", func(c *gin.Context) {
		s.serveStaticFileGin(c, "admin.html")
	})

	// Favicon
	s.router.GET("/favicon.ico", func(c *gin.Context) {
		s.serveStaticFileGin(c, "assets/favicon.avif")
	})

	// Socket.IO 端点
	handler := s.socketServer.ServeHandler(nil)
	s.router.GET("/socket.io/*any", gin.WrapH(handler))
	s.router.POST("/socket.io/*any", gin.WrapH(handler))
}

// serveStaticFileGin 为 gin.Context 提供静态文件服务
func (s *Server) serveStaticFileGin(c *gin.Context, filename string) {
	if s.staticFS != nil {
		f, err := s.staticFS.Open(filename)
		if err != nil {
			c.String(http.StatusNotFound, filename+" not found")
			return
		}
		defer f.Close()
		content, err := readAll(f)
		if err != nil {
			c.String(http.StatusInternalServerError, "failed to read "+filename)
			return
		}

		contentType := getContentType(filename)
		c.Data(http.StatusOK, contentType, content)
	} else {
		c.String(http.StatusOK, "Frontend not loaded")
	}
}

// SetStatic 设置静态文件处理
func (s *Server) SetStatic(fs http.FileSystem) {
	s.router.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if len(path) >= 4 && path[:4] == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not Found"})
			return
		}

		cleanPath := path
		if len(cleanPath) > 0 && cleanPath[0] == '/' {
			cleanPath = cleanPath[1:]
		}

		f, err := fs.Open(cleanPath)
		if err == nil {
			f.Close()
			s.serveStaticFileGin(c, cleanPath)
			return
		}

		if strings.HasPrefix(path, "/dashboard") {
			s.serveStaticFileGin(c, "admin.html")
		} else {
			s.serveStaticFileGin(c, "index.html")
		}
	})
}

// getContentType 根据文件扩展名返回 Content-Type
func getContentType(filename string) string {
	switch {
	case strings.HasSuffix(filename, ".js"):
		return "application/javascript"
	case strings.HasSuffix(filename, ".css"):
		return "text/css"
	case strings.HasSuffix(filename, ".avif"):
		return "image/avif"
	case strings.HasSuffix(filename, ".ico"):
		return "image/x-icon"
	case strings.HasSuffix(filename, ".png"):
		return "image/png"
	case strings.HasSuffix(filename, ".svg"):
		return "image/svg+xml"
	default:
		return "text/html; charset=utf-8"
	}
}

// readAll 从 io.Reader 读取所有内容
func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var result []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			result = append(result, buf[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return result, err
		}
	}
	return result, nil
}
