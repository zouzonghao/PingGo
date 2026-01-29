package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"ping-go/config"
	"ping-go/db"
	"ping-go/model"
	"ping-go/notification"
	"ping-go/pkg/logger"
	"regexp"
	"strings"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"go.uber.org/zap"
)

const (
	MinMonitorInterval = 20
	DefaultPingTimeout = 5 * time.Second
)

type NotificationJob struct {
	Name      string
	OldStatus int
	NewStatus int
	Message   string
}

type Service struct {
	monitors            map[uint]*model.Monitor
	tickers             map[uint]*time.Ticker
	stopChans           map[uint]chan struct{}
	mu                  sync.Mutex
	OnHeartbeat         func(h *model.Heartbeat)
	notificationChannel chan *NotificationJob
	stopWorker          chan struct{}
	workerStopped       bool
	stoppedMonitors     map[uint]bool
}

func NewService() *Service {
	// Init logger if not already
	logger.Init("info")

	// Reset all notifications to inactive on startup
	if err := db.DB.Model(&model.Notification{}).Where("1=1").Update("active", false).Error; err != nil {
		logger.Error("Failed to reset notification active status", zap.Error(err))
	}

	s := &Service{
		monitors:            make(map[uint]*model.Monitor),
		tickers:             make(map[uint]*time.Ticker),
		stopChans:           make(map[uint]chan struct{}),
		notificationChannel: make(chan *NotificationJob, 100),
		stopWorker:          make(chan struct{}),
		stoppedMonitors:     make(map[uint]bool),
	}

	go s.runNotificationWorker()
	go s.runScheduledWorker()
	return s
}

func (s *Service) Shutdown(ctx context.Context) error {
	logger.Info("Shutting down monitor service...")

	// Stop notification worker
	if !s.workerStopped {
		close(s.stopWorker)
		s.workerStopped = true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id := range s.monitors {
		if stopChan, ok := s.stopChans[id]; ok {
			close(stopChan)
		}
		if ticker, ok := s.tickers[id]; ok {
			ticker.Stop()
		}
	}

	return nil
}

func (s *Service) HealthCheck() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	return map[string]any{
		"total_monitors":  len(s.monitors),
		"active_monitors": len(s.tickers),
		"status":          "healthy",
	}
}

func (s *Service) runNotificationWorker() {
	for {
		select {
		case job := <-s.notificationChannel:
			// 1. Check DB Trigger Rules
			var rules []model.Notification
			if err := db.DB.Where("type = ? AND active = ?", "trigger", true).Find(&rules).Error; err == nil && len(rules) > 0 {
				for _, rule := range rules {
					var cfg struct {
						MonitorName string `json:"monitor_name"`
						OnStatus    string `json:"on_status"` // "down", "up", "change"
						Email       string `json:"email"`
					}
					if err := json.Unmarshal([]byte(rule.Config), &cfg); err != nil {
						continue
					}

					// Check Monitor Name Match ("*" means all)
					if cfg.MonitorName != "*" && cfg.MonitorName != job.Name {
						continue
					}

					// Check Status Condition
					shouldSend := false
					if cfg.OnStatus == "change" {
						shouldSend = true
					} else if cfg.OnStatus == "down" && job.NewStatus == model.StatusDown {
						shouldSend = true
					} else if cfg.OnStatus == "up" && job.NewStatus == model.StatusUp {
						shouldSend = true
					}

					if shouldSend && cfg.Email != "" {
						to := []string{cfg.Email}
						subject := fmt.Sprintf("PingGo Notification: %s is %s", job.Name, statusToString(job.NewStatus))
						content := fmt.Sprintf("Monitor <b>%s</b> changed status from <b>%s</b> to <b>%s</b>.<br>Message: %s<br>Time: %s",
							job.Name, statusToString(job.OldStatus), statusToString(job.NewStatus), job.Message, time.Now().Format("2006-01-02 15:04:05"))

						go func(recipients []string, subj, body string) {
							if err := notification.SendEmail(recipients, subj, body); err != nil {
								logger.Error("Failed to send trigger email", zap.Strings("recipients", recipients), zap.Error(err))
							}
						}(to, subject, content)
					}
				}
			}

			// 2. Global Config Fallback (Legacy) - REMOVED
			// We only send notifications if the user has explicitly configured a Trigger rule.
			// This prevents "surprise" emails when the user hasn't set up any alerts.
			/*
				toEmail := config.GlobalConfig.Notification.Email
				if toEmail == "" {
					toEmail = os.Getenv("NOTIFICATION_EMAIL")
				}

				if toEmail != "" {
					// Legacy behavior removed to fix bug where users receive emails without setting triggers.
				}
			*/

		case <-s.stopWorker:
			return
		}
	}
}

func (s *Service) runScheduledWorker() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			nowStr := time.Now().Format("15:04")
			var rules []model.Notification
			if err := db.DB.Where("type = ? AND active = ?", "schedule", true).Find(&rules).Error; err == nil {
				for _, rule := range rules {
					var cfg struct {
						Time  string `json:"time"`
						Email string `json:"email"`
					}
					if err := json.Unmarshal([]byte(rule.Config), &cfg); err != nil {
						continue
					}

					if cfg.Time == nowStr {
						// Send Report
						if cfg.Email != "" {
							go s.sendReport(cfg.Email)
						}
					}
				}
			}
		case <-s.stopWorker:
			return
		}
	}
}

func (s *Service) sendReport(email string) {
	// Gather stats
	s.mu.Lock()
	total := len(s.monitors)
	up := 0
	down := 0
	paused := 0

	type MonitorInfo struct {
		Name           string
		Status         string
		Color          string
		Type           string
		Uptime24h      float64
		AvgResponse24h int64
	}
	var monitorList []MonitorInfo

	for _, m := range s.monitors {
		if m.Active != 1 {
			paused++
			continue
		}

		statusStr := "UNKNOWN"
		color := "#95a5a6" // grey for unknown

		switch m.Status {
		case model.StatusUp:
			up++
			statusStr = "正常"
			color = "#2ecc71" // green
		case model.StatusDown:
			down++
			statusStr = "异常"
			color = "#e74c3c" // red
		case model.StatusPending:
			statusStr = "检测中"
			color = "#f1c40f" // yellow
		case model.StatusMaintenance:
			statusStr = "维护"
			color = "#3498db" // blue
		}

		// Calculate 24h stats
		uptime24h := db.GetUptimeStats(m.ID, 24*time.Hour)
		avgResp24h := db.GetAvgResponseTime(m.ID, 24*time.Hour)

		monitorList = append(monitorList, MonitorInfo{
			Name:           m.Name,
			Status:         statusStr,
			Color:          color,
			Type:           string(m.Type),
			Uptime24h:      uptime24h,
			AvgResponse24h: int64(avgResp24h),
		})
	}
	s.mu.Unlock()

	// Calculate overview percentage
	uptimePercent := 0.0
	activeCount := total - paused
	if activeCount > 0 {
		uptimePercent = float64(up) / float64(activeCount) * 100.0
	}

	dateStr := time.Now().Format("2006-01-02")
	subject := fmt.Sprintf("PingGo 日报 - %s", dateStr)

	// Build HTML Content
	html := fmt.Sprintf(`
	<!DOCTYPE html>
	<html>
	<head>
		<meta charset="utf-8">
		<meta name="viewport" content="width=device-width, initial-scale=1.0">
	</head>
	<body style="margin: 0; padding: 0; background-color: #f6f9fc; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;">
		<div style="max-width: 600px; margin: 0 auto; background-color: #ffffff; border-radius: 12px; overflow: hidden; box-shadow: 0 4px 6px rgba(0,0,0,0.05); margin-top: 20px; margin-bottom: 20px;">
			<!-- Header -->
			<div style="background-color: #2ecc71; padding: 30px 40px; text-align: center;">
				<h1 style="margin: 0; color: #ffffff; font-size: 24px; font-weight: 700; letter-spacing: 0.5px;">PingGo 每日速报</h1>
				<p style="margin: 10px 0 0; color: rgba(255,255,255,0.9); font-size: 14px;">%s</p>
			</div>

			<!-- Summary Cards -->
			<div style="padding: 30px 40px; background-color: #f8f9fa; border-bottom: 1px solid #edf2f7;">
				<div style="display: grid; grid-template-columns: repeat(3, 1fr); gap: 15px; text-align: center;">
					<div style="background: white; padding: 15px; border-radius: 8px; border: 1px solid #e2e8f0;">
						<div style="font-size: 12px; color: #64748b; text-transform: uppercase; font-weight: 600;">监控总数</div>
						<div style="font-size: 24px; font-weight: 800; color: #1e293b; margin-top: 5px;">%d</div>
					</div>
					<div style="background: white; padding: 15px; border-radius: 8px; border: 1px solid #e2e8f0;">
						<div style="font-size: 12px; color: #64748b; text-transform: uppercase; font-weight: 600;">系统在线率</div>
						<div style="font-size: 24px; font-weight: 800; color: #2ecc71; margin-top: 5px;">%.1f%%</div>
					</div>
					<div style="background: white; padding: 15px; border-radius: 8px; border: 1px solid #e2e8f0;">
						<div style="font-size: 12px; color: #64748b; text-transform: uppercase; font-weight: 600;">异常服务</div>
						<div style="font-size: 24px; font-weight: 800; color: %s; margin-top: 5px;">%d</div>
					</div>
				</div>
			</div>

			<!-- Detail List -->
			<div style="padding: 30px 40px;">
				<h3 style="margin: 0 0 20px; color: #334155; font-size: 16px; font-weight: 700;">监控详情</h3>
				<table style="width: 100%%; border-collapse: collapse;">
					<thead style="background-color: #f8f9fa; color: #64748b; font-size: 12px; text-transform: uppercase; text-align: left;">
						<tr>
							<th style="padding: 12px 15px; border-radius: 6px 0 0 6px;">服务名称</th>
							<th style="padding: 12px 15px; text-align: center;">24h 在线率</th>
							<th style="padding: 12px 15px; text-align: center;">平均延迟</th>
							<th style="padding: 12px 15px; text-align: right; border-radius: 0 6px 6px 0;">状态</th>
						</tr>
					</thead>
					<tbody style="font-size: 14px; color: #334155;">
	`, dateStr, activeCount, uptimePercent, func() string {
		if down > 0 {
			return "#e74c3c"
		}
		return "#94a3b8"
	}(), down)

	for index, m := range monitorList {
		rowBg := "#ffffff"
		if index%2 == 1 {
			rowBg = "#f8f9fa" // Zebra striping
		}

		// Color logic for uptime
		uptimeColor := "#2ecc71"
		if m.Uptime24h < 90 {
			uptimeColor = "#e74c3c"
		} else if m.Uptime24h < 99 {
			uptimeColor = "#f1c40f"
		}

		html += fmt.Sprintf(`
						<tr style="background-color: %s;">
							<td style="padding: 12px 15px; border-bottom: 1px solid #f1f5f9;">
								<div style="font-weight: 600;">%s</div>
								<div style="font-size: 11px; color: #94a3b8; margin-top: 2px;">%s</div>
							</td>
							<td style="padding: 12px 15px; border-bottom: 1px solid #f1f5f9; text-align: center; font-family: monospace; font-weight: 600; color: %s;">
								%.1f%%
							</td>
							<td style="padding: 12px 15px; border-bottom: 1px solid #f1f5f9; text-align: center; font-family: monospace;">
								%d ms
							</td>
							<td style="padding: 12px 15px; text-align: right; border-bottom: 1px solid #f1f5f9;">
								<span style="display: inline-block; padding: 4px 10px; border-radius: 20px; font-size: 12px; font-weight: 600; background-color: %s15; color: %s;">
									%s
								</span>
							</td>
						</tr>
		`, rowBg, m.Name, strings.ToUpper(m.Type), uptimeColor, m.Uptime24h, m.AvgResponse24h, m.Color, m.Color, m.Status)
	}

	html += `
					</tbody>
				</table>
			</div>

			<!-- Footer -->
			<div style="padding: 20px 40px; background-color: #f1f5f9; text-align: center; border-bottom-left-radius: 12px; border-bottom-right-radius: 12px;">
				<p style="margin: 0; color: #94a3b8; font-size: 12px;">
					PingGo Monitor System &bull; <a href="#" style="color: #94a3b8; text-decoration: none;">Manage Notifications</a>
				</p>
			</div>
		</div>
	</body>
	</html>
	`

	if err := notification.SendEmail([]string{email}, subject, html); err != nil {
		logger.Error("Failed to send report", zap.String("email", email), zap.Error(err))
	}
}

func (s *Service) Start() {
	var monitors []model.Monitor
	result := db.DB.Find(&monitors)
	if result.Error != nil {
		logger.Error("Failed to load monitors", zap.Error(result.Error))
		return
	}

	for _, m := range monitors {
		if m.Active == 1 {
			// Copy variable to avoid loop scope issues
			monitor := m
			s.StartMonitor(&monitor)
		}
	}
}

func (s *Service) StartMonitor(m *model.Monitor) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Reset stopped status
	delete(s.stoppedMonitors, m.ID)

	// Stop existing if any
	if stopChan, ok := s.stopChans[m.ID]; ok {
		delete(s.stopChans, m.ID) // Delete first to prevent double close in race
		close(stopChan)
	}
	if t, ok := s.tickers[m.ID]; ok {
		t.Stop()
		delete(s.tickers, m.ID)
	}

	s.monitors[m.ID] = m

	if m.Active != 1 {
		logger.Info("Monitor is inactive, skipping", zap.String("name", m.Name))
		return
	}

	if m.Interval < MinMonitorInterval {
		m.Interval = MinMonitorInterval
	}

	ticker := time.NewTicker(time.Duration(m.Interval) * time.Second)
	stopChan := make(chan struct{})
	s.tickers[m.ID] = ticker
	s.stopChans[m.ID] = stopChan

	go func() {
		// Run immediately once
		s.Check(m.ID)
		for {
			select {
			case <-ticker.C:
				s.Check(m.ID)
			case <-stopChan:
				return // Exit goroutine gracefully
			}
		}
	}()
	logger.Info("Started monitoring", zap.String("name", m.Name), zap.String("url", m.URL))
}

func (s *Service) StopMonitor(id uint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stoppedMonitors[id] {
		return
	}

	if m, ok := s.monitors[id]; ok {
		logger.Info("Stopped monitoring", zap.String("name", m.Name), zap.String("url", m.URL))
	}

	if stopChan, ok := s.stopChans[id]; ok {
		delete(s.stopChans, id) // Delete first
		close(stopChan)
	}
	if t, ok := s.tickers[id]; ok {
		t.Stop()
		delete(s.tickers, id)
	}
	delete(s.monitors, id)
	s.stoppedMonitors[id] = true
}

func (s *Service) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Stop notification worker (only once)
	if !s.workerStopped {
		close(s.stopWorker)
		s.workerStopped = true
	}

	for id, stopChan := range s.stopChans {
		delete(s.stopChans, id)
		close(stopChan)
	}
	for id, t := range s.tickers {
		t.Stop()
		delete(s.tickers, id)
	}
}

func (s *Service) Check(id uint) {
	// Retrieve fresh copy or use cached? Better get fresh to ensure no race on updating status
	var m model.Monitor
	if err := db.DB.First(&m, id).Error; err != nil {
		return
	}

	if m.Active != 1 {
		s.StopMonitor(m.ID)
		return
	}

	var status int
	var msg string
	var duration int
	startTime := time.Now()

	switch m.Type {
	case model.MonitorTypeHTTP:
		status, msg = CheckHTTP(m)
		duration = int(time.Since(startTime).Milliseconds())
	case model.MonitorTypePing:
		var rtt time.Duration
		status, msg, rtt = CheckPing(m.URL, m.Timeout)
		duration = int(rtt.Milliseconds())
	case model.MonitorTypeTCP:
		var tcpDuration time.Duration
		status, msg, tcpDuration = CheckTCP(m.URL, m.Timeout)
		duration = int(tcpDuration.Milliseconds())
	case model.MonitorTypeDNS:
		status, msg = CheckDNS(m.URL, m.Timeout)
		duration = int(time.Since(startTime).Milliseconds())
	default:
		// Default to HTTP if unknown or fallback
		if m.Type == "" {
			status, msg = CheckHTTP(m)
			duration = int(time.Since(startTime).Milliseconds())
		} else {
			status, msg = model.StatusDown, fmt.Sprintf("Unsupported type: %s", m.Type)
			duration = 0
		}
	}

	if m.Status != model.StatusPending && m.Status != status {
		logger.Info("Monitor status changed",
			zap.String("name", m.Name),
			zap.Int("old_status", m.Status),
			zap.Int("new_status", status),
		)

		// Copy for callback
		mCopy := m
		mCopy.Status = status
		mCopy.Message = msg
		mCopy.LastCheck = time.Now()

		// Send Email via Worker Pool
		select {
		case s.notificationChannel <- &NotificationJob{
			Name:      m.Name,
			OldStatus: m.Status,
			NewStatus: status,
			Message:   msg,
		}:
		default:
			logger.Warn("Notification channel full, dropping alert")
		}
	}

	// Update DB
	m.Status = status
	m.Message = msg
	m.LastCheck = time.Now()

	db.DB.Save(&m)

	// Save Heartbeat asynchronously via buffer
	heartbeat := model.Heartbeat{
		MonitorID: m.ID,
		Status:    status,
		Message:   msg,
		Time:      m.LastCheck,
		Duration:  duration,
	}
	db.AddHeartbeat(&heartbeat)

	// Notify via callback (Socket.IO) - Every check
	if s.OnHeartbeat != nil {
		s.OnHeartbeat(&heartbeat)
	}

	logger.Info("Check finished",
		zap.String("name", m.Name),
		zap.String("type", string(m.Type)),
		zap.Int("status", status),
		zap.String("msg", msg),
	)
}

func statusToString(status int) string {
	switch status {
	case model.StatusUp:
		return "UP"
	case model.StatusDown:
		return "DOWN"
	case model.StatusPending:
		return "PENDING"
	case model.StatusMaintenance:
		return "MAINTENANCE"
	default:
		return "UNKNOWN"
	}
}

var defaultTransport = &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 10,
	IdleConnTimeout:     90 * time.Second,
	DisableKeepAlives:   false,
	DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{
			Timeout:   0,                // Rely on context timeout
			KeepAlive: 30 * time.Second, // Keep-alive is fine to stay at 30s as it doesn't affect detection timeout
			Resolver:  getCustomResolver(),
		}
		return dialer.DialContext(ctx, network, addr)
	},
}

func getCustomResolver() *net.Resolver {
	dnsServer := config.GlobalConfig.Monitor.DNSServer

	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: 2 * time.Second,
			}

			// If user configured a specific DNS, use it
			if dnsServer != "" {
				addr := dnsServer
				if !strings.Contains(addr, ":") {
					addr += ":53"
				}
				return d.DialContext(ctx, "udp", addr)
			}

			// Default logic: Try Google DNS first, then Alidns
			conn, err := d.DialContext(ctx, "udp", "1.1.1.1:53")
			if err == nil {
				return conn, nil
			}

			// Fallback to Alidns
			return d.DialContext(ctx, "udp", "223.5.5.5:53")
		},
	}
}

var (
	httpClient           *http.Client
	httpClientNoRedirect *http.Client
	httpClientOnce       sync.Once
)

func initHTTPClients() {
	httpClientOnce.Do(func() {
		httpClient = &http.Client{
			Transport: defaultTransport,
			Timeout:   600 * time.Second, // 10 minutes max as safety net (actual timeout via context)
		}
		httpClientNoRedirect = &http.Client{
			Transport: defaultTransport,
			Timeout:   600 * time.Second, // 10 minutes max as safety net
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	})
}

func getHTTPClient(followRedirects bool) *http.Client {
	initHTTPClients()
	if followRedirects {
		return httpClient
	}
	return httpClientNoRedirect
}

func CheckHTTP(m model.Monitor) (int, string) {
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 10
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	method := m.Method
	if method == "" {
		method = "GET"
	}

	var body io.Reader
	contentType := ""

	isFormMethod := strings.EqualFold(method, "POST") || strings.EqualFold(method, "PUT") || strings.EqualFold(method, "PATCH")
	if isFormMethod && m.FormData != "" {
		var fields []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
			Type  string `json:"type"` // "text" or "file"
		}
		if err := json.Unmarshal([]byte(m.FormData), &fields); err == nil && len(fields) > 0 {
			bodyBuffer := &bytes.Buffer{}
			writer := multipart.NewWriter(bodyBuffer)
			for _, field := range fields {
				if field.Type == "file" {
					// Security Check: Force relative path and disallow traversing up
					if filepath.IsAbs(field.Value) || strings.Contains(field.Value, "..") {
						return model.StatusDown, fmt.Sprintf("Invalid file path: %s (must be relative and cannot contain '..')", field.Value)
					}

					// Read file from current working directory
					wd, _ := os.Getwd()
					filePath := filepath.Join(wd, field.Value)

					file, err := os.Open(filePath)
					if err != nil {
						return model.StatusDown, fmt.Sprintf("Open file failed: %v", err)
					}
					part, err := writer.CreateFormFile(field.Key, filepath.Base(filePath))
					if err != nil {
						file.Close()
						return model.StatusDown, fmt.Sprintf("Create form file failed: %v", err)
					}
					_, err = io.Copy(part, file)
					file.Close()
					if err != nil {
						return model.StatusDown, fmt.Sprintf("Copy file content failed: %v", err)
					}
				} else {
					_ = writer.WriteField(field.Key, field.Value)
				}
			}
			writer.Close()
			body = bodyBuffer
			contentType = writer.FormDataContentType()
		}
	}

	if body == nil && m.Body != "" {
		body = strings.NewReader(m.Body)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.URL, body)
	if err != nil {
		return model.StatusDown, fmt.Sprintf("Create request failed: %v", err)
	}

	client := getHTTPClient(m.FollowRedirects)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Add Headers
	if m.Headers != "" {
		var headers map[string]string
		err := json.Unmarshal([]byte(m.Headers), &headers)
		if err == nil && len(headers) > 0 {
			// JSON format
			for k, v := range headers {
				if contentType != "" && strings.EqualFold(k, "Content-Type") {
					continue
				}
				req.Header.Set(k, v)
			}
		} else {
			// Legacy K=V format: KEY=VALUE,KEY=VALUE
			pairs := strings.Split(m.Headers, ",")
			for _, pair := range pairs {
				kv := strings.SplitN(pair, "=", 2)
				if len(kv) == 2 {
					key := strings.TrimSpace(kv[0])
					value := strings.TrimSpace(kv[1])
					if key != "" {
						req.Header.Set(key, value)
					}
				}
			}
		}
	}

	// Default User-Agent if not set
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "PingGo-Monitor/1.0")
	}

	resp, err := client.Do(req)
	if err != nil {
		// Simplify common errors
		errStr := err.Error()
		if strings.Contains(errStr, "deadline exceeded") || strings.Contains(errStr, "Client.Timeout") {
			return model.StatusDown, "Timeout"
		}
		if strings.Contains(errStr, "connection refused") {
			return model.StatusDown, "Connection Refused"
		}
		if strings.Contains(errStr, "no such host") {
			return model.StatusDown, "DNS Resolution Failed"
		}
		if strings.Contains(errStr, "remote error: tls") {
			return model.StatusDown, "TLS Error"
		}
		// Truncate long error messages
		if len(errStr) > 40 {
			return model.StatusDown, errStr[:37] + "..."
		}
		return model.StatusDown, errStr
	}
	defer resp.Body.Close()

	// Check Status
	// Check Status
	statusOk := true
	var errorMsg string

	if m.ExpectedStatus > 0 {
		if resp.StatusCode != m.ExpectedStatus {
			statusOk = false
			errorMsg = fmt.Sprintf("Status %d (Expected %d)", resp.StatusCode, m.ExpectedStatus)
		}
	} else {
		// Default 2xx check
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			statusOk = false
			errorMsg = fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
		}
	}

	// Helper function for body truncation
	truncateBody := func(b string) string {
		maxLen := 300
		if len(b) > maxLen {
			return b[:maxLen] + "...(truncated)"
		}
		return b
	}

	if !statusOk {
		// Helper: If POST request fails, append body for debugging
		if m.Method == "POST" {
			// Read up to 10KB (enough for most error JSONs)
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 10240))
			if len(bodyBytes) > 0 {
				bodyStr := strings.TrimSpace(string(bodyBytes))
				if bodyStr != "" {
					errorMsg += fmt.Sprintf(" Body: %s", truncateBody(bodyStr))
				}
			}
		}
		return model.StatusDown, errorMsg
	}

	// Check Regex
	// 响应正则验证：数据库中存储的始终是正则表达式（JSON 输入已在服务端转换）
	if m.ResponseRegex != "" {
		// Read body (limit to 1MB)
		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		if err != nil {
			return model.StatusDown, fmt.Sprintf("Read body failed: %v", err)
		}
		bodyStr := string(bodyBytes)

		matched, err := regexp.MatchString(m.ResponseRegex, bodyStr)
		if err != nil {
			return model.StatusDown, fmt.Sprintf("Regex error: %v", err)
		}
		if !matched {
			msg := "响应不匹配！"
			if len(bodyStr) > 0 {
				msg += fmt.Sprintf(" Body: %s", truncateBody(strings.TrimSpace(bodyStr)))
			}
			return model.StatusDown, msg
		}
	}

	msg := fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	if m.ResponseRegex != "" {
		msg += "，正则匹配成功！"
	}
	return model.StatusUp, msg
}

func CheckPing(addr string, timeoutSec int) (int, string, time.Duration) {
	pinger, err := probing.NewPinger(addr)
	if err != nil {
		return model.StatusDown, fmt.Sprintf("Init ping failed: %v", err), 0
	}

	// Windows need privileged or UDP.
	// On Windows, raw socket requires Admin. UDP is safer but might be blocked.
	// We try privileged = true (requires running as Admin on Windows).
	if os.Getenv("OS") == "Windows_NT" {
		pinger.SetPrivileged(true)
	}

	pinger.Count = 3
	pinger.Interval = 100 * time.Millisecond // Reduce wait between packets

	timeout := time.Duration(timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = DefaultPingTimeout
	}
	pinger.Timeout = timeout

	err = pinger.Run() // blocks
	if err != nil {
		return model.StatusDown, fmt.Sprintf("Ping failed: %v", err), 0
	}

	stats := pinger.Statistics()
	if stats.PacketsRecv == 0 {
		return model.StatusDown, "100% packet loss", 0
	}

	msg := fmt.Sprintf("%.2f ms", float64(stats.AvgRtt.Microseconds())/1000.0)
	if stats.PacketLoss > 0 {
		msg += fmt.Sprintf(" (%.0f%% loss)", stats.PacketLoss)
	}

	return model.StatusUp, msg, stats.AvgRtt
}

func CheckTCP(addr string, timeoutSec int) (int, string, time.Duration) {
	timeout := time.Duration(timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := net.Dialer{
		Timeout:  timeout,
		Resolver: getCustomResolver(),
	}
	start := time.Now()
	conn, err := dialer.Dial("tcp", addr)
	duration := time.Since(start)

	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "connection refused") {
			return model.StatusDown, "Connection Refused", 0
		}
		if strings.Contains(errStr, "i/o timeout") {
			return model.StatusDown, "Timeout", 0
		}
		return model.StatusDown, "Connection Failed", 0
	}
	defer conn.Close()

	msg := fmt.Sprintf("Port Open (%.2f ms)", float64(duration.Microseconds())/1000.0)
	return model.StatusUp, msg, duration
}

func CheckDNS(domain string, timeoutSec int) (int, string) {
	timeout := time.Duration(timeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	resolver := getCustomResolver()
	if resolver == nil {
		resolver = net.DefaultResolver
	}

	ips, err := resolver.LookupIP(ctx, "ip", domain)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "no such host") {
			return model.StatusDown, "Host Not Found"
		}
		if strings.Contains(errStr, "i/o timeout") {
			return model.StatusDown, "Timeout"
		}
		if strings.Contains(errStr, "server failure") {
			return model.StatusDown, "Server Failure"
		}
		// Truncate long error messages
		if len(errStr) > 40 {
			return model.StatusDown, errStr[:37] + "..."
		}
		return model.StatusDown, errStr
	}
	if len(ips) == 0 {
		return model.StatusDown, "No IP found"
	}
	return model.StatusUp, fmt.Sprintf("IP: %v", ips[0])
}

// TestHTTP performs a request but returns the raw status code and body for testing purposes.
func TestHTTP(m model.Monitor) (int, string) {
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 10
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	method := m.Method
	if method == "" {
		method = "GET"
	}

	var body io.Reader
	contentType := ""

	isFormMethod := strings.EqualFold(method, "POST") || strings.EqualFold(method, "PUT") || strings.EqualFold(method, "PATCH")
	if isFormMethod && m.FormData != "" {
		var fields []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
			Type  string `json:"type"` // "text" or "file"
		}
		if err := json.Unmarshal([]byte(m.FormData), &fields); err == nil && len(fields) > 0 {
			bodyBuffer := &bytes.Buffer{}
			writer := multipart.NewWriter(bodyBuffer)
			for _, field := range fields {
				if field.Type == "file" {
					// Security Check: Force relative path and disallow traversing up
					if filepath.IsAbs(field.Value) || strings.Contains(field.Value, "..") {
						return 0, fmt.Sprintf("Invalid file path: %s (must be relative and cannot contain '..')", field.Value)
					}
					// Read file from current working directory
					wd, _ := os.Getwd()
					filePath := filepath.Join(wd, field.Value)

					file, err := os.Open(filePath)
					if err != nil {
						return 0, fmt.Sprintf("Open file failed: %v", err)
					}
					part, err := writer.CreateFormFile(field.Key, filepath.Base(filePath))
					if err != nil {
						file.Close()
						return 0, fmt.Sprintf("Create form file failed: %v", err)
					}
					_, err = io.Copy(part, file)
					file.Close()
					if err != nil {
						return 0, fmt.Sprintf("Copy file content failed: %v", err)
					}
				} else {
					_ = writer.WriteField(field.Key, field.Value)
				}
			}
			writer.Close()
			body = bodyBuffer
			contentType = writer.FormDataContentType()
		}
	}

	if body == nil && m.Body != "" {
		body = strings.NewReader(m.Body)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.URL, body)
	if err != nil {
		return 0, fmt.Sprintf("Create request failed: %v", err)
	}

	client := getHTTPClient(m.FollowRedirects)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Add Headers
	if m.Headers != "" {
		var headers map[string]string
		err := json.Unmarshal([]byte(m.Headers), &headers)
		if err == nil && len(headers) > 0 {
			for k, v := range headers {
				if contentType != "" && strings.EqualFold(k, "Content-Type") {
					continue
				}
				req.Header.Set(k, v)
			}
		} else {
			pairs := strings.Split(m.Headers, ",")
			for _, pair := range pairs {
				kv := strings.SplitN(pair, "=", 2)
				if len(kv) == 2 {
					key := strings.TrimSpace(kv[0])
					value := strings.TrimSpace(kv[1])
					if key != "" {
						req.Header.Set(key, value)
					}
				}
			}
		}
	}

	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "PingGo-Monitor/1.0")
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()

	// Read body (limit to 10KB for test preview)
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10240))
	if err != nil {
		return resp.StatusCode, fmt.Sprintf("Read body failed: %v", err)
	}

	return resp.StatusCode, string(bodyBytes)
}
