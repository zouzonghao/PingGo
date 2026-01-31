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

type CheckResult struct {
	MonitorID uint
	Name      string
	URL       string
	Status    int
	Message   string
}

type NotificationState struct {
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	LastSentStatus       int
}

type Service struct {
	monitors           map[uint]*model.Monitor
	tickers            map[uint]*time.Ticker
	stopChans          map[uint]chan struct{}
	mu                 sync.Mutex
	OnHeartbeat        func(h *model.Heartbeat)
	checkResultChannel chan *CheckResult
	stopWorker         chan struct{}
	workerStopped      bool
	stoppedMonitors    map[uint]bool
	notificationStates map[string]*NotificationState
}

func NewService() *Service {
	// Init logger if not already
	logger.Init("info")

	// Reset trigger notifications to inactive on startup as requested
	if err := db.DB.Model(&model.Notification{}).Where("type = ?", "trigger").Update("active", false).Error; err != nil {
		logger.Error("Failed to reset trigger notifications", zap.Error(err))
	}

	s := &Service{
		monitors:           make(map[uint]*model.Monitor),
		tickers:            make(map[uint]*time.Ticker),
		stopChans:          make(map[uint]chan struct{}),
		checkResultChannel: make(chan *CheckResult, 1000),
		stopWorker:         make(chan struct{}),
		stoppedMonitors:    make(map[uint]bool),
		notificationStates: make(map[string]*NotificationState),
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
	logger.Info("Notification worker started")
	for {
		select {
		case result := <-s.checkResultChannel:
			// 1. Check DB Trigger Rules
			var rules []model.Notification
			if err := db.DB.Where("type = ? AND active = ?", "trigger", true).Find(&rules).Error; err == nil && len(rules) > 0 {
				for _, rule := range rules {
					var cfg struct {
						MonitorName        string `json:"monitor_name"`
						OnStatus           string `json:"on_status"` // "down", "up", "change"
						Email              string `json:"email"`
						MaxRetries         int    `json:"max_retries"`
						MaxRetriesRecovery int    `json:"max_retries_recovery"`
					}
					if err := json.Unmarshal([]byte(rule.Config), &cfg); err != nil {
						logger.Error("Failed to unmarshal trigger config", zap.Error(err))
						continue
					}

					// Check Monitor Name Match ("*" means all)
					if cfg.MonitorName != "*" && cfg.MonitorName != result.Name {
						continue
					}

					// State Management Key
					stateKey := fmt.Sprintf("%d_%d", rule.ID, result.MonitorID)

					s.mu.Lock()
					state, exists := s.notificationStates[stateKey]
					if !exists {
						state = &NotificationState{
							LastSentStatus: result.Status, // Initialize with current status to arm immediately
						}
						s.notificationStates[stateKey] = state
						s.mu.Unlock()
						// First time sync, no notification needed yet
						continue
					}

					// Update Counters
					// Only count Success/Failure for definitive statuses.
					// Pending (and others) should not reset/increment counters.
					if result.Status == model.StatusDown {
						state.ConsecutiveFailures++
						state.ConsecutiveSuccesses = 0
					} else if result.Status == model.StatusUp {
						state.ConsecutiveSuccesses++
						state.ConsecutiveFailures = 0
					}

					// Determine Effective Status (Hard Status)
					shouldNotify := false
					newStatusToSend := state.LastSentStatus

					// Define thresholds (0 treated as 1 for immediate)
					thresholdDown := cfg.MaxRetries
					if thresholdDown <= 0 {
						thresholdDown = 1
					}
					thresholdUp := cfg.MaxRetriesRecovery
					if thresholdUp <= 0 {
						thresholdUp = 1
					}

					if result.Status == model.StatusDown {
						if state.ConsecutiveFailures >= thresholdDown {
							newStatusToSend = model.StatusDown
						}
					} else if result.Status == model.StatusUp {
						if state.ConsecutiveSuccesses >= thresholdUp {
							newStatusToSend = model.StatusUp
						}
					} else {
						// Maintenance / Pending usually immediate? or treat as UP for now?
						// STRICT LOGIC: Do not change Hard Status during Pending
						// newStatusToSend = result.Status (Removed to keep previous hard status)
					}

					if newStatusToSend != state.LastSentStatus {
						// Status Changed!
						shouldNotify = false
						if cfg.OnStatus == "change" {
							shouldNotify = true
						} else if cfg.OnStatus == "down" && newStatusToSend == model.StatusDown {
							shouldNotify = true
						} else if cfg.OnStatus == "up" && newStatusToSend == model.StatusUp {
							shouldNotify = true
						}

						// Update State
						state.LastSentStatus = newStatusToSend

						// Release lock before sending notification (although send is async, let's minimize lock time)
						s.mu.Unlock()

						if shouldNotify {
							// Send Notification
							s.sendTriggerNotification(cfg.Email, result.Name, result.URL, state.LastSentStatus, newStatusToSend, result.Message)
						}
					} else {
						s.mu.Unlock()
					}
				}
			} else if err != nil {
				logger.Error("Failed to fetch trigger rules", zap.Error(err))
			}

		case <-s.stopWorker:
			logger.Info("Notification worker stopped")
			return
		}
	}
}

func (s *Service) sendTriggerNotification(email, name, url string, oldStatus, newStatus int, msg string) {
	if email == "" {
		return
	}
	to := []string{email}
	subject := fmt.Sprintf("PingGo Notification: %s is %s", name, statusToString(newStatus))
	// Determine style
	color := "#e74c3c" // Red for error
	statusText := "服务宕机通知"
	if newStatus == model.StatusUp {
		color = "#2ecc71" // Green for recovery
		statusText = "服务恢复通知"
	}

	data := notification.StatusChangeData{
		Name:       name,
		URL:        url,
		OldStatus:  statusToString(oldStatus),
		NewStatus:  statusToString(newStatus),
		Message:    msg,
		Color:      color,
		StatusText: statusText,
		DateTime:   time.Now().Format("2006-01-02 15:04:05"),
	}

	content, err := notification.RenderStatusChangeEmail(data)
	if err != nil {
		logger.Error("Failed to render status change email", zap.Error(err))
		return
	}

	logger.Info("Sending trigger email", zap.Strings("to", to), zap.String("subject", subject))
	go func(recipients []string, subj, body string) {
		if err := notification.SendEmail(recipients, subj, body); err != nil {
			logger.Error("Failed to send trigger email", zap.Strings("recipients", recipients), zap.Error(err))
		} else {
			logger.Info("Trigger email sent successfully", zap.Strings("recipients", recipients))
		}
	}(to, subject, content)
}

func (s *Service) runScheduledWorker() {
	logger.Info("Scheduled worker started")
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var rules []model.Notification
			if err := db.DB.Where("type = ? AND active = ?", "schedule", true).Find(&rules).Error; err == nil {
				for _, rule := range rules {
					var cfg struct {
						Time     string `json:"time"`
						Email    string `json:"email"`
						Timezone string `json:"timezone"`
					}
					if err := json.Unmarshal([]byte(rule.Config), &cfg); err != nil {
						continue
					}

					// Handle Timezone
					now := time.Now()
					if cfg.Timezone != "" {
						loc, err := time.LoadLocation(cfg.Timezone)
						if err == nil {
							now = now.In(loc)
						} else {
							logger.Error("Failed to load location", zap.String("timezone", cfg.Timezone), zap.Error(err))
						}
					}
					nowStr := now.Format("15:04")

					if cfg.Time == nowStr {
						logger.Info("Triggering scheduled report", zap.String("email", cfg.Email), zap.String("time", nowStr), zap.String("timezone", cfg.Timezone))
						// Send Report
						if cfg.Email != "" {
							go s.sendReport(cfg.Email)
						}
					}
				}
			}
		case <-s.stopWorker:
			logger.Info("Scheduled worker stopped")
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

	downColor := "#94a3b8"
	if down > 0 {
		downColor = "#e74c3c"
	}

	// Prepare monitor list for template
	var reportMonitors []notification.MonitorInfo
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

		reportMonitors = append(reportMonitors, notification.MonitorInfo{
			Name:           m.Name,
			Type:           strings.ToUpper(m.Type),
			Uptime24h:      m.Uptime24h,
			AvgResponse24h: m.AvgResponse24h,
			Status:         m.Status,
			Color:          m.Color,
			UptimeColor:    uptimeColor,
			RowBg:          rowBg,
		})
	}

	data := notification.DailyReportData{
		Date:          dateStr,
		TotalCount:    activeCount,
		UptimePercent: uptimePercent,
		DownCount:     down,
		DownColor:     downColor,
		Monitors:      reportMonitors,
	}

	html, err := notification.RenderDailyReportEmail(data)
	if err != nil {
		logger.Error("Failed to render daily report email", zap.Error(err))
		return
	}

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

	// Clean up states for this monitor?
	// The problem is keys are string "RuleID_MonitorID"
	// We should probably iterate and delete.
	for key := range s.notificationStates {
		if strings.HasSuffix(key, fmt.Sprintf("_%d", id)) {
			delete(s.notificationStates, key)
		}
	}

	s.stoppedMonitors[id] = true
}

func (s *Service) ResetNotificationState(ruleID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prefix := fmt.Sprintf("%d_", ruleID)
	for key := range s.notificationStates {
		if strings.HasPrefix(key, prefix) {
			delete(s.notificationStates, key)
		}
	}
	logger.Info("Reset notification memory state for rule", zap.Uint("ruleID", ruleID))
}

func (s *Service) ResetNotificationStateByMonitor(monitorID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	suffix := fmt.Sprintf("_%d", monitorID)
	for key := range s.notificationStates {
		if strings.HasSuffix(key, suffix) {
			delete(s.notificationStates, key)
		}
	}
	logger.Info("Reset notification memory state for monitor", zap.Uint("monitorID", monitorID))
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

	// Reset all states
	s.notificationStates = make(map[string]*NotificationState)
}

func (s *Service) Check(id uint) {
	// Retrieve fresh copy
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
		// 如果是超时或网络连接类的硬故障，将时长设为 0，以便前端图表显示为虚线
		if status == model.StatusDown && (msg == "Timeout" || msg == "Connection Refused" || msg == "DNS Resolution Failed" || msg == "TLS Error") {
			duration = 0
		}
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
		// DNS 失败通常视为硬故障
		if status == model.StatusDown {
			duration = 0
		}
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

	// Always update DB with raw status
	m.Status = status
	m.Message = msg
	m.LastCheck = time.Now()

	// Only update status fields to avoid overwriting Active state if changed concurrently
	db.DB.Model(&m).Select("Status", "Message", "LastCheck").Updates(&m)

	// Save Heartbeat
	heartbeat := model.Heartbeat{
		MonitorID: m.ID,
		Status:    status,
		Message:   msg,
		Time:      m.LastCheck,
		Duration:  duration,
	}
	db.AddHeartbeat(&heartbeat)

	// Notify via callback (Socket.IO)
	if s.OnHeartbeat != nil {
		s.OnHeartbeat(&heartbeat)
	}

	// Send to Notification Worker
	select {
	case s.checkResultChannel <- &CheckResult{
		MonitorID: m.ID,
		Name:      m.Name,
		URL:       m.URL,
		Status:    status,
		Message:   msg,
	}:
	default:
		logger.Warn("Check result channel full, dropping result")
	}

	logger.Info("Check finished",
		zap.String("name", m.Name),
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
		maxLen := 10000
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

	// Read body (limit to 50KB for test preview)
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 51200))
	if err != nil {
		return resp.StatusCode, fmt.Sprintf("Read body failed: %v", err)
	}

	return resp.StatusCode, string(bodyBytes)
}
