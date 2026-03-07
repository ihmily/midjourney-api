package model

import (
	"time"

	"gorm.io/gorm"
)

type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "PENDING"
	TaskStatusSubmitted  TaskStatus = "SUBMITTED"
	TaskStatusInQueue    TaskStatus = "IN_QUEUE"
	TaskStatusProcessing TaskStatus = "PROCESSING"
	TaskStatusSuccess    TaskStatus = "SUCCESS"
	TaskStatusFailed     TaskStatus = "FAILED"
	TaskStatusTimeout    TaskStatus = "TIMEOUT"
)

type TaskType string

const (
	TaskTypeImagine         TaskType = "IMAGINE"
	TaskTypeUpscale         TaskType = "UPSCALE"          // Upscale
	TaskTypeZoomOut2x       TaskType = "ZOOM_OUT_2X"      // Zoom Out 2x
	TaskTypeZoomOut1_5x     TaskType = "ZOOM_OUT_1_5X"    // Zoom Out 1.5x
	TaskTypeUpscaleSubtle   TaskType = "UPSCALE_SUBTLE"   // Upscale (Subtle)
	TaskTypeUpscaleCreative TaskType = "UPSCALE_CREATIVE" // Upscale (Creative)
)

type Task struct {
	ID               uint           `gorm:"primaryKey" json:"id"`
	TaskID           string         `gorm:"uniqueIndex;size:64;not null" json:"task_id"`
	UserID           uint           `gorm:"index;not null" json:"user_id"`
	AccountID        *uint          `gorm:"index" json:"account_id,omitempty"`
	ParentTaskID     string         `gorm:"size:64;index" json:"parent_task_id,omitempty"` // Parent task ID, used for upscale/variation etc. subtasks
	Type             TaskType       `gorm:"size:32;not null" json:"type"`
	Prompt           string         `gorm:"type:text" json:"prompt,omitempty"`
	Status           TaskStatus     `gorm:"size:32;default:'PENDING';index:idx_status_created" json:"status"`
	Progress         int            `gorm:"default:0" json:"progress"`
	DiscordMessageID string         `gorm:"size:64;index" json:"discord_message_id,omitempty"`
	ImageURL         string         `gorm:"type:text" json:"image_url,omitempty"`
	OSSImageURL      string         `gorm:"type:text" json:"oss_image_url,omitempty"`
	ErrorMessage     string         `gorm:"type:text" json:"error_message,omitempty"`
	Buttons          *string        `gorm:"type:json" json:"buttons,omitempty"`
	CreatedAt        time.Time      `gorm:"index:idx_status_created" json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`
}

func (Task) TableName() string {
	return "tasks"
}
