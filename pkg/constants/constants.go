package constants

import "time"

// Task related constants
const (
	DefaultConcurrentLimit = 20

	MaxAccountGuildIDLength = 64

	MaxAccountChannelIDLength = 64

	MaxAccountUserTokenLength = 512

	MaxTaskIDLength = 64

	MaxDiscordMessageIDLength = 64

	MinTaskProgress = 0

	MaxTaskProgress = 100

	MaxErrorCount = 3

	DefaultListLimit = 10

	MaxListLimit = 100

	TaskMatchTimeWindow = 5 * time.Minute

	ActionTaskMatchTimeWindow = 2 * time.Minute

	QueuePollTimeout = 5 * time.Second

	DefaultTaskTimeout = 5 * time.Minute

	ListenerStartupWait = 2 * time.Second

	ListenerOperationTimeout = 10 * time.Second

	DefaultHTTPTimeout = 30 * time.Second

	CallbackTimeout = 10 * time.Second

	CallbackMaxAttempts = 3

	CallbackRetryBaseDelay = 200 * time.Millisecond

	WorkerStopTimeout = 10 * time.Second

	TimeoutSweepBatchSize = 100

	QueueInspectLimit int64 = 100

	MaxRequestBodyBytes int64 = 1 * 1024 * 1024

	MaxImageDownloadBytes int64 = 25 * 1024 * 1024

	ServerReadTimeout = 10 * time.Second

	ServerReadHeaderTimeout = 5 * time.Second

	ServerWriteTimeout = 60 * time.Second

	ServerIdleTimeout = 120 * time.Second

	ServerMaxHeaderBytes = 1 << 20
)

const (
	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"

	// DiscordOrigin Discord Origin
	DiscordOrigin = "https://discord.com"
)
