package model

import (
	"time"

	"gorm.io/gorm"
)

type MonitorType string

const (
	MonitorTypeHTTP MonitorType = "http"
	MonitorTypePing MonitorType = "ping"
	MonitorTypeTCP  MonitorType = "tcp"
	MonitorTypeDNS  MonitorType = "dns"
)

const (
	StatusDown        = 0
	StatusUp          = 1
	StatusPending     = 2
	StatusMaintenance = 3
)

type Monitor struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Name     string      `json:"name"`
	URL      string      `json:"url"` // For HTTP/TCP
	Type     MonitorType `json:"type"`
	Method   string      `json:"method" gorm:"default:'GET'"`
	Body     string      `json:"body"`
	Headers  string      `json:"headers"` // JSON string
	
	Timeout         int    `json:"timeout" gorm:"default:10"`
	ExpectedStatus  int    `json:"expected_status" gorm:"default:0"` // 0 means 2xx
	ResponseRegex   string `json:"response_regex"`
	FollowRedirects bool   `json:"follow_redirects" gorm:"default:true"`

	Interval int         `json:"interval"` // In seconds

	Active int `json:"active" gorm:"default:1"`
	Weight int `json:"weight" gorm:"default:2000"`

	Status    int       `json:"status"` // 0: DOWN, 1: UP, 2: PENDING, 3: MAINTENANCE
	LastCheck time.Time `json:"last_check"`
	Message   string    `json:"msg"` // Frontend expects "msg" not "message" usually? checking.. Uptime Kuma uses "msg" in heartbeat, but "message" in monitor? Let's check heartbeat.
}

type User struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	Username  string         `gorm:"uniqueIndex" json:"username"`
	Password  string         `json:"-"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

type Setting struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	Key   string `gorm:"uniqueIndex" json:"key"`
	Value string `json:"value"`
	Type  string `json:"type"`
}

type Notification struct {
	ID     uint           `gorm:"primaryKey" json:"id"`
	Name   string         `json:"name"`
	Type   string         `json:"type"` // always "email" for now
	Config string         `json:"config"` // JSON string of config
	Active bool           `json:"active" gorm:"default:true"`
	UserID uint           `json:"userId"`
}

type Heartbeat struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	MonitorID uint      `gorm:"index:idx_monitor_time" json:"monitorID"`
	Status    int       `json:"status"` // 0: DOWN, 1: UP, 2: PENDING, 3: MAINTENANCE
	Message   string    `json:"msg"`
	Time      time.Time `gorm:"index:idx_monitor_time" json:"time"`
	Duration  int       `json:"duration"` // response time in ms
}
