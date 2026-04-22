package model

import (
	"time"

	"gorm.io/gorm"
)

type AccountStatus string

type AccountHealth string

const (
	AccountStatusActive   AccountStatus = "ACTIVE"
	AccountStatusDisabled AccountStatus = "DISABLED"
	AccountStatusBanned   AccountStatus = "BANNED"
)

const (
	AccountHealthHealthy    AccountHealth = "HEALTHY"    // Account is healthy, can login and draw
	AccountHealthUnhealthy  AccountHealth = "UNHEALTHY"  // Account is unhealthy, cannot draw
	AccountHealthQuarantine AccountHealth = "QUARANTINE" // Account is quarantined
	AccountHealthUnknown    AccountHealth = "UNKNOWN"    // Account status is unknown
)

type Account struct {
	ID              uint           `gorm:"primaryKey" json:"id"`
	GuildID         string         `gorm:"size:64;not null" json:"guild_id"`
	ChannelID       string         `gorm:"size:64;not null" json:"channel_id"`
	UserToken       string         `gorm:"size:512;not null" json:"user_token"`
	Status          AccountStatus  `gorm:"size:32;default:'ACTIVE'" json:"status"`
	Health          AccountHealth  `gorm:"size:32;default:'UNKNOWN'" json:"health"`
	ConcurrentLimit int            `gorm:"default:3" json:"concurrent_limit"`
	CurrentJobs     int            `gorm:"default:0" json:"current_jobs"`
	LastHeartbeat   *time.Time     `json:"last_heartbeat,omitempty"`
	LastUsedAt      *time.Time     `json:"last_used_at,omitempty"`
	LastError       string         `gorm:"size:512" json:"last_error,omitempty"`
	ErrorCount      int            `gorm:"default:0" json:"error_count"`
	SuccessCount    int            `gorm:"default:0" json:"success_count"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
}

func (Account) TableName() string {
	return "accounts"
}
