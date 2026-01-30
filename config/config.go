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

// defaultConfigContent 默认配置文件内容
const defaultConfigContent = `
server:
  port: 37374
notification:
  resend_api_key: "YOUR_RESEND_API_KEY"
  email: "YOUR_EMAIL@example.com"

# 数据保留配置 - 分层存储策略
# 原始数据保留较短时间，聚合数据保留较长时间，大幅节省存储空间
retention:
  raw_hours: 24      # 原始心跳数据保留 24 小时
  hourly_days: 7     # 小时级聚合数据保留 7 天
  daily_days: 365    # 日级聚合数据保留 1 年
`

func LoadConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 配置文件不存在，生成默认配置文件
			if writeErr := os.WriteFile(path, []byte(defaultConfigContent), 0644); writeErr != nil {
				return fmt.Errorf("failed to create default config file: %w", writeErr)
			}
			// 使用默认配置内容
			data = []byte(defaultConfigContent)
			fmt.Printf("Configuration file not found. Created default config at: %s\n", path)
		} else {
			return err
		}
	}

	if err := yaml.Unmarshal(data, &GlobalConfig); err != nil {
		return err
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
