package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// RetentionConfig 数据保留配置
// 用于控制分层数据存储的保留时间
type RetentionConfig struct {
	RawHours   int `yaml:"raw_hours"`   // 原始心跳数据保留小时数，默认 24
	HourlyDays int `yaml:"hourly_days"` // 小时聚合数据保留天数，默认 7
	DailyDays  int `yaml:"daily_days"`  // 日聚合数据保留天数，默认 365
}

type Config struct {
	Server       ServerConfig       `yaml:"server"`
	Notification NotificationConfig `yaml:"notification"`
	Monitor      MonitorConfig      `yaml:"monitor"`
	Retention    RetentionConfig    `yaml:"retention"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type NotificationConfig struct {
	ResendAPIKey string `yaml:"resend_api_key"`
	Email        string `yaml:"email"`
	FromEmail    string `yaml:"from_email"`
	FromName     string `yaml:"from_name"`
}

type MonitorConfig struct {
	DNSServer string `yaml:"dns_server"`
}

var GlobalConfig Config

func LoadConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		// If file doesn't exist, we might still be okay if env vars are set
		if !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := yaml.Unmarshal(data, &GlobalConfig); err != nil {
			return err
		}
	}

	// Environment variable overrides
	if apiKey := os.Getenv("RESEND_API_KEY"); apiKey != "" {
		GlobalConfig.Notification.ResendAPIKey = apiKey
	}
	if email := os.Getenv("NOTIFICATION_EMAIL"); email != "" {
		GlobalConfig.Notification.Email = email
	}
	if port := os.Getenv("PORT"); port != "" {
		var p int
		fmt.Sscanf(port, "%d", &p)
		if p != 0 {
			GlobalConfig.Server.Port = p
		}
	}

	return nil
}
