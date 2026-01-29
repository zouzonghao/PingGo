package model

import "time"

type Session struct {
	Token     string    `gorm:"primaryKey" json:"token"`
	UserID    uint      `gorm:"index" json:"userId"`
	ExpiresAt time.Time `gorm:"index" json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}
