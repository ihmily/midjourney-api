package model

import (
	"time"

	"gorm.io/gorm"
)

type UserStatus string

const (
	UserStatusActive   UserStatus = "ACTIVE"
	UserStatusDisabled UserStatus = "DISABLED"
)

type User struct {
	ID           uint           `gorm:"primaryKey" json:"id"`
	Username     string         `gorm:"uniqueIndex;size:128;not null" json:"username"`
	Email        string         `gorm:"uniqueIndex;size:256" json:"email,omitempty"`
	PasswordHash string         `gorm:"size:256" json:"-"`
	APIKey       string         `gorm:"uniqueIndex;size:64" json:"api_key"`
	RateLimit    int            `gorm:"default:100" json:"rate_limit"` // 每小时请求次数
	Status       UserStatus     `gorm:"size:32;default:'ACTIVE'" json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

func (User) TableName() string {
	return "users"
}
