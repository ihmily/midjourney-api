package model

import (
	"time"

	"gorm.io/gorm"
)

type Account struct {
	ID              uint           `gorm:"primaryKey" json:"id"`
	GuildID         string         `gorm:"size:64;not null" json:"guild_id"`
	ChannelID       string         `gorm:"size:64;not null" json:"channel_id"`
	UserToken       string         `gorm:"size:512;not null" json:"-"`
	IsDisabled      bool           `gorm:"default:false" json:"is_disabled"`
	IsHealthy       bool           `gorm:"default:false" json:"is_healthy"`
	ConcurrentLimit int            `gorm:"default:20" json:"concurrent_limit"`
	CurrentJobs     int            `gorm:"default:0" json:"current_jobs"`
	LastUsedAt      *time.Time     `json:"last_used_at,omitempty"`
	LastError       string         `gorm:"size:512" json:"-"`
	ErrorCount      int            `gorm:"default:0" json:"error_count"`
	SuccessCount    int            `gorm:"default:0" json:"success_count"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
}

func (Account) TableName() string {
	return "accounts"
}
