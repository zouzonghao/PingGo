package server

import (
	"io/fs"
	"log"
	"net/http"
	"os"
	"ping-go/db"
	"ping-go/model"
	"ping-go/monitor"
	"strings"
	"text/template"
	"time"

	"github.com/benbjohnson/hashfs"
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
	staticRoot     fs.FS
	hfs            *hashfs.FS
}

// NewServer 创建并初始化一个新的服务器实例
func NewServer(monitorService *monitor.Service, staticFS http.FileSystem, staticRoot fs.FS) *Server {
	s := &Server{
		router:         gin.Default(),
		socketServer:   socket.NewServer(nil, nil),
		monitorService: monitorService,
		staticFS:       staticFS,
		staticRoot:     staticRoot,
		hfs:            hashfs.NewFS(staticRoot),
	}

	// 启动会话清理任务
	go startSessionCleanup()

	// 配置 CORS
	corsConfig := cors.Config{
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}

	if os.Getenv("DEBUG") == "true" {
		corsConfig.AllowOrigins = []string{"*"}
	} else {
		corsConfig.AllowOriginFunc = func(origin string) bool {
			return true
		}
	}
	s.router.Use(cors.New(corsConfig))

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

	// 注册 API 路由
	s.registerAPIRoutes()
	// 注册 Socket.IO 路由
	s.registerRoutes()

	return s
}

// registerAPIRoutes 注册 REST API 路由
func (s *Server) registerAPIRoutes() {
	api := s.router.Group("/api")
	{
		api.GET("/monitors", s.getMonitorsAPI)
		api.POST("/monitors", s.createMonitorAPI)
		api.DELETE("/monitors/:id", s.deleteMonitorAPI)
	}
}

// Router 返回 Gin 引擎实例
func (s *Server) Router() *gin.Engine {
	return s.router
}

// registerRoutes 注册 Socket.IO 相关的路由
func (s *Server) registerRoutes() {
	// Socket.IO 端点
	handler := s.socketServer.ServeHandler(nil)
	s.router.GET("/socket.io/*any", gin.WrapH(handler))
	s.router.POST("/socket.io/*any", gin.WrapH(handler))

	// 设置 Socket.IO 连接处理
	s.socketServer.On("connection", func(args ...any) {
		client := args[0].(*socket.Socket)
		client.Join("public")
		s.setupAuthHandlers(client)
		s.setupMonitorHandlers(client)
		s.setupHeartbeatHandlers(client)
		s.setupNotificationHandlers(client)
		s.setupSettingsHandlers(client)
	})
}

// serveStaticFileGin 为 gin.Context 提供静态文件服务
func (s *Server) serveStaticFileGin(c *gin.Context, filename string) {
	if s.staticRoot != nil {
		// 检查是否是 HTML 文件或目录请求（通常是 index.html）
		isHTML := strings.HasSuffix(filename, ".html")
		targetFile := filename
		if filename == "" || filename == "/" {
			isHTML = true
			targetFile = "index.html"
		}

		if isHTML {
			// 如果是 HTML，作为模板处理，以便注入 hashfs
			content, err := fs.ReadFile(s.staticRoot, targetFile)
			if err != nil {
				// 如果直接读取失败，可能是子目录请求，交给 FileServer
				http.FileServer(http.FS(s.hfs)).ServeHTTP(c.Writer, c.Request)
				return
			}

			// 使用 text/template 提高对 HTML/JS 的兼容性
			tmpl, err := template.New(targetFile).Parse(string(content))

			if err != nil {
				log.Printf("failed to parse template %s: %v. Falling back to raw content.", targetFile, err)
				c.Header("Content-Type", "text/html; charset=utf-8")
				c.Writer.Write(content)
				return
			}

			// 构造模板数据，支持 {{ .Hash "path" }}
			data := map[string]any{
				"Static": s.hfs,
			}

			c.Header("Content-Type", "text/html; charset=utf-8")
			c.Header("Cache-Control", "no-cache")
			c.Status(http.StatusOK) // 显式设置 200 状态码，防止 NoRoute 默认返回 404
			err = tmpl.Execute(c.Writer, data)
			if err != nil {
				log.Printf("failed to execute template %s: %v", targetFile, err)
			}
			return
		}

		// 对于非 HTML 文件，使用 hashfs 处理
		// 如果文件名包含哈希，就设置强缓存
		_, hash := hashfs.ParseName(filename)
		if hash != "" && os.Getenv("DEBUG") != "true" {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			c.Header("Cache-Control", "no-cache")
		}
		http.FileServer(http.FS(s.hfs)).ServeHTTP(c.Writer, c.Request)
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

		// 使用 s.hfs.Open 而不是 fs.Open，因为 s.hfs 支持带哈希的文件名
		f, err := s.hfs.Open(cleanPath)
		if err == nil {
			f.Close()
			s.serveStaticFileGin(c, cleanPath)
			return
		}

		// SPA 路由处理
		if path == "favicon.ico" {
			c.Status(http.StatusNotFound)
			return
		}

		if strings.HasPrefix(path, "/dashboard") {
			s.serveStaticFileGin(c, "admin.html")
		} else {
			s.serveStaticFileGin(c, "index.html")
		}
	})
}
