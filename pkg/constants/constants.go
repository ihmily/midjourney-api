package constants

import "time"

// Task related constants
const (
	DefaultConcurrentLimit = 3

	MaxErrorCount = 3

	TaskMatchTimeWindow = 5 * time.Minute

	ActionTaskMatchTimeWindow = 2 * time.Minute

	QueuePollTimeout = 5 * time.Second

	ListenerStartupWait = 2 * time.Second
)

const (
	DefaultUserID uint = 1
)

const (
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"

	// DiscordOrigin Discord Origin
	DiscordOrigin = "https://discord.com"
)
