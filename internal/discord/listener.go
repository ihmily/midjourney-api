package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/oss"
	"github.com/trae/midjourney-api/internal/repository"
	"github.com/trae/midjourney-api/internal/safehttp"
	"github.com/trae/midjourney-api/pkg/constants"
	"github.com/trae/midjourney-api/pkg/redact"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ossUploadImagineResult controls whether the /imagine 2x2 grid image is uploaded to OSS.
// Set to false to skip OSS upload for imagine results; set to true to enable.
const ossUploadImagineResult = false

const matchedMessageTTL = 30 * time.Minute

var defaultCallbackHTTPClient = newCallbackHTTPClient()

func newCallbackHTTPClient() *http.Client {
	return safehttp.NewPublicClient(constants.CallbackTimeout, "callback", validateCallbackURL)
}

func validateCallbackURL(rawURL string) error {
	_, err := normalizeCallbackURL(rawURL)
	return err
}

func normalizeCallbackURL(rawURL string) (string, error) {
	return safehttp.NormalizePublicHTTPURL(rawURL, "callback URL")
}

func listenerOperationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), constants.ListenerOperationTimeout)
}

// Use discordgo for Discord events
type Listener struct {
	session         *discordgo.Session
	parser          *MessageParser
	taskRepo        repository.TaskRepository
	accountRepo     repository.AccountRepository
	ossUploader     oss.Uploader
	callbackClient  *http.Client
	logger          *zap.Logger
	midjourneyBotID string
	matchedMsgIDs   map[string]time.Time
	msgMutex        sync.RWMutex
}

func NewListener(botToken, midjourneyBotID string, db *gorm.DB, logger *zap.Logger, ossUploader oss.Uploader) *Listener {
	if logger == nil {
		logger = zap.NewNop()
	}
	if db == nil {
		logger.Error("Failed to create Discord listener: database is required")
		return nil
	}

	// Create Discord session
	session, err := discordgo.New("Bot " + botToken)
	if err != nil {
		logger.Error("Failed to create Discord session", zap.Error(err))
		return nil
	}

	listener := &Listener{
		session:         session,
		parser:          NewMessageParser(midjourneyBotID, logger),
		taskRepo:        repository.NewTaskRepository(db),
		accountRepo:     repository.NewAccountRepository(db),
		ossUploader:     ossUploader,
		callbackClient:  defaultCallbackHTTPClient,
		logger:          logger,
		midjourneyBotID: midjourneyBotID,
		matchedMsgIDs:   make(map[string]time.Time),
	}

	// Set Intents (must include message content permission)
	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent

	session.AddHandler(listener.handleMessageCreate)
	session.AddHandler(listener.handleMessageUpdate)

	return listener
}

func (l *Listener) Start() error {
	if l == nil || l.session == nil {
		return fmt.Errorf("discord listener is not initialized")
	}

	// Open WebSocket connection
	err := l.session.Open()
	if err != nil {
		if l.logger != nil {
			l.logger.Error("Failed to open Discord connection", zap.Error(err))
		}
		return err
	}

	return nil
}

func (l *Listener) GetBotInfo() (username string, userID string) {
	if l.session != nil && l.session.State != nil && l.session.State.User != nil {
		return l.session.State.User.Username, l.session.State.User.ID
	}
	return "", ""
}

func (l *Listener) Stop() error {
	if l == nil || l.session == nil {
		return nil
	}
	if l.logger != nil {
		l.logger.Info("Stopping Discord listener...")
	}
	return l.session.Close()
}

func (l *Listener) handleMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if l == nil || l.parser == nil || l.taskRepo == nil || m == nil || m.Message == nil ||
		m.Author == nil || m.Author.ID != l.midjourneyBotID {
		return
	}
	if strings.TrimSpace(m.ID) == "" {
		return
	}

	parsed := l.parser.ParseDiscordMessage(m.Message)
	if parsed == nil {
		return
	}

	ctx, cancel := listenerOperationContext()
	defer cancel()

	task, err := l.taskRepo.GetByDiscordMessageID(ctx, m.ID)
	if err == nil && task != nil {
		l.updateTask(task, parsed)
		return
	}

	l.updateMatchingTask(parsed, m.Message)
}

func (l *Listener) handleMessageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if l == nil || l.parser == nil || l.taskRepo == nil || m == nil || m.Message == nil {
		return
	}
	if strings.TrimSpace(m.ID) == "" {
		return
	}

	// Author may be nil in partial Discord updates; only skip if author is explicitly NOT the MJ bot
	if m.Author != nil && m.Author.ID != l.midjourneyBotID {
		return
	}

	parsed := l.parser.ParseDiscordMessage(m.Message)
	if parsed == nil {
		return
	}

	ctx, cancel := listenerOperationContext()
	defer cancel()

	task, err := l.taskRepo.GetByDiscordMessageID(ctx, m.ID)
	if err == nil && task != nil {
		l.updateTask(task, parsed)
		return
	}

	l.updateMatchingTask(parsed, m.Message)
}

func (l *Listener) isMessageMatched(msgID string) bool {
	l.msgMutex.RLock()
	matchedAt, ok := l.matchedMsgIDs[msgID]
	l.msgMutex.RUnlock()
	return ok && time.Since(matchedAt) <= matchedMessageTTL
}

func (l *Listener) updateMatchingTask(parsed *ParsedMessage, msg *discordgo.Message) {
	if l == nil || l.taskRepo == nil || l.accountRepo == nil || l.parser == nil || parsed == nil || msg == nil {
		return
	}
	if strings.TrimSpace(parsed.MessageID) == "" {
		return
	}

	if l.isMessageMatched(parsed.MessageID) {
		return
	}

	// Handle describe result messages
	if parsed.IsDescribeResult {
		l.matchDescribeTask(msg.ChannelID, parsed)
		return
	}

	if msg.MessageReference != nil && msg.MessageReference.MessageID != "" {
		if matched := l.matchActionTaskByReference(msg.MessageReference.MessageID, parsed); matched {
			return
		}
	}

	if parsed.TaskPrompt == "" {
		return
	}

	ctx, cancel := listenerOperationContext()
	defer cancel()

	tasks, err := l.taskRepo.GetDiscordActiveTasks(ctx, 50)
	if err != nil {
		l.logger.Error("Failed to find tasks", zap.Error(err))
		return
	}

	for _, task := range tasks {
		if task == nil {
			continue
		}

		if time.Since(task.CreatedAt) > constants.TaskMatchTimeWindow {
			continue
		}

		if model.IsTerminalTaskStatus(task.Status) {
			continue
		}

		if task.AccountID != nil {
			account, err := l.accountRepo.GetByID(ctx, *task.AccountID)
			if err != nil || account == nil {
				continue
			}

			if account.ChannelID != msg.ChannelID {
				continue
			}

			// Situation 1: imagine task matching (direct prompt matching)
			if task.Type == model.TaskTypeImagine {
				matched := l.parser.MatchTaskByPrompt(parsed.TaskPrompt, task.Prompt)
				if !matched {
					l.logger.Debug("[Imagine task match failed]",
						zap.String("task_id", task.TaskID),
						zap.Int("parsed_prompt_length", runeCount(parsed.TaskPrompt)),
						zap.Int("task_prompt_length", runeCount(task.Prompt)),
					)
				} else {
					l.matchAndUpdateTask(task, parsed)
					return
				}
			}

			// Situation 2: action sub-task matching (upscale, zoom_out etc.)
			if task.ParentTaskID != "" {
				parentTask, err := l.taskRepo.GetByTaskID(ctx, task.ParentTaskID)
				if err == nil && parentTask != nil && parentTask.DiscordMessageID != "" {
					if l.parser.MatchTaskByPrompt(parsed.TaskPrompt, parentTask.Prompt) {
						l.logger.Info("[Match action sub-task - via prompt]",
							zap.String("task_id", task.TaskID),
							zap.String("parent_task_id", task.ParentTaskID),
							zap.String("type", string(task.Type)))
						l.matchAndUpdateTask(task, parsed)
						return
					}
				}
			}
		}
	}

	l.logger.Debug("[No matching task found]",
		zap.String("message_id", msg.ID),
		zap.String("channel_id", msg.ChannelID),
		zap.Int("prompt_length", runeCount(parsed.TaskPrompt)))
}

func (l *Listener) matchActionTaskByReference(parentMessageID string, parsed *ParsedMessage) bool {
	if l == nil || l.taskRepo == nil || l.parser == nil || parsed == nil {
		return false
	}
	parentMessageID = strings.TrimSpace(parentMessageID)
	parsed.MessageID = strings.TrimSpace(parsed.MessageID)
	if parentMessageID == "" || parsed.MessageID == "" {
		return false
	}

	ctx, cancel := listenerOperationContext()
	defer cancel()

	parentTask, err := l.taskRepo.GetByDiscordMessageID(ctx, parentMessageID)
	if err != nil || parentTask == nil {
		return false
	}

	tasks, err := l.taskRepo.GetDiscordActiveTasks(ctx, 50)
	if err != nil {
		return false
	}

	for _, task := range tasks {
		if task == nil {
			continue
		}

		if task.ParentTaskID == parentTask.TaskID {
			if time.Since(task.CreatedAt) > constants.ActionTaskMatchTimeWindow {
				continue
			}

			if task.DiscordMessageID != "" && task.DiscordMessageID != parsed.MessageID {
				continue
			}

			if model.IsTerminalTaskStatus(task.Status) {
				continue
			}

			if task.DiscordMessageID == "" {
				l.logger.Info("[First match action task]",
					zap.String("task_id", task.TaskID),
					zap.String("parent_task_id", task.ParentTaskID),
					zap.String("type", string(task.Type)),
					zap.Int("progress", parsed.Progress))
			}
			l.matchAndUpdateTask(task, parsed)
			return true
		}
	}

	return false
}

func (l *Listener) matchAndUpdateTask(task *model.Task, parsed *ParsedMessage) {
	if task.DiscordMessageID != parsed.MessageID {
		oldMessageID := task.DiscordMessageID
		task.DiscordMessageID = parsed.MessageID
		ctx, cancel := listenerOperationContext()
		err := l.taskRepo.Update(ctx, task)
		cancel()
		if err != nil {
			l.logger.Error("Failed to update task", zap.Error(err))
			return
		}
		if oldMessageID != "" {
			l.logger.Info("[Update message ID]",
				zap.String("task_id", task.TaskID),
				zap.String("old_message_id", oldMessageID),
				zap.String("new_message_id", parsed.MessageID))
		}
		l.markMessageMatched(parsed.MessageID)
	}

	l.updateTask(task, parsed)
}

func (l *Listener) updateTask(task *model.Task, parsed *ParsedMessage) {
	if task == nil || parsed == nil {
		return
	}
	if model.IsTerminalTaskStatus(task.Status) {
		l.logger.Debug("Ignoring update for terminal task",
			zap.String("task_id", task.TaskID),
			zap.String("current_status", string(task.Status)),
			zap.String("parsed_status", parsed.Status))
		return
	}

	var guildID, channelID string
	if task.AccountID != nil && l.accountRepo != nil {
		ctx, cancel := listenerOperationContext()
		account, err := l.accountRepo.GetByID(ctx, *task.AccountID)
		cancel()
		if err == nil && account != nil {
			guildID = account.GuildID
			channelID = account.ChannelID
		}
	}

	oldStatus := task.Status
	oldProgress := task.Progress
	terminalTransition := false
	shouldCallback := false
	shouldUploadOSS := false
	hasTaskUpdate := false

	switch parsed.Status {
	case "pending":
		if task.Status == model.TaskStatusSubmitted {
			task.Status = model.TaskStatusInQueue
			task.Progress = 0
			hasTaskUpdate = true
			shouldCallback = true
			l.logger.Info("[In queue]",
				zap.String("Task ID", task.TaskID),
				zap.String("MID", parsed.MessageID),
				zap.String("Guild ID", guildID),
				zap.String("Channel ID", channelID),
			)
		} else if task.Status == model.TaskStatusInQueue && task.Progress != 0 {
			task.Progress = 0
			hasTaskUpdate = true
		}
	case "processing":
		if task.Status != model.TaskStatusProcessing {
			task.Status = model.TaskStatusProcessing
			hasTaskUpdate = true
			shouldCallback = true
		}
		if oldProgress != parsed.Progress {
			task.Progress = parsed.Progress
			hasTaskUpdate = true
			shouldCallback = true
			l.logger.Info("[Processing]",
				zap.String("Task ID", task.TaskID),
				zap.String("MID", parsed.MessageID),
				zap.String("Guild ID", guildID),
				zap.String("Channel ID", channelID),
				zap.Int("Progress", parsed.Progress),
			)
		}
	case "completed":
		if task.Status == model.TaskStatusSuccess {
			return
		}
		task.Status = model.TaskStatusSuccess
		task.Progress = parsed.Progress
		task.ImageURL = parsed.ImageURL
		now := time.Now()
		task.FinishedAt = &now
		terminalTransition = !model.IsTerminalTaskStatus(oldStatus)
		hasTaskUpdate = true
		shouldCallback = true
		l.logger.Info("[Completed]",
			zap.String("Task ID", task.TaskID),
			zap.String("MID", parsed.MessageID),
			zap.String("Guild ID", guildID),
			zap.String("Channel ID", channelID),
			zap.String("Image URL", redact.URL(parsed.ImageURL)),
		)

		if l.ossUploader != nil && parsed.ImageURL != "" && (ossUploadImagineResult || task.Type != model.TaskTypeImagine) {
			shouldUploadOSS = true
		}
	case "failed":
		if task.Status == model.TaskStatusFailed {
			return
		}
		task.Status = model.TaskStatusFailed
		task.Progress = failedTaskProgress(task.Progress, parsed.Progress)
		if task.ErrorMessage == "" {
			task.ErrorMessage = "task failed"
		}
		now := time.Now()
		task.FinishedAt = &now
		terminalTransition = !model.IsTerminalTaskStatus(oldStatus)
		hasTaskUpdate = true
		shouldCallback = true
		l.logger.Error("[Failed]",
			zap.String("Task ID", task.TaskID),
			zap.String("MID", parsed.MessageID),
			zap.String("Guild ID", guildID),
			zap.String("Channel ID", channelID),
		)
	default:
		hasTaskUpdate = false
	}

	if buttonsJSON, changed := parsedButtonsJSON(parsed.Buttons, task.Buttons); changed {
		buttonsStr := buttonsJSON
		task.Buttons = &buttonsStr
		shouldCallback = true
		hasTaskUpdate = true
	}

	if !hasTaskUpdate {
		return
	}

	ctx, cancel := listenerOperationContext()
	defer cancel()

	if terminalTransition {
		transitioned, err := l.taskRepo.UpdateTerminal(ctx, task)
		if err != nil {
			l.logger.Error("Failed to update terminal task", zap.Error(err))
			return
		}
		if !transitioned {
			l.logger.Debug("Terminal task update skipped because task is already terminal",
				zap.String("task_id", task.TaskID),
				zap.String("status", string(task.Status)))
			return
		}
		l.decrementAccountJobs(task)
		if task.Status == model.TaskStatusFailed {
			l.recordAccountTaskFailure(task)
		} else if task.Status == model.TaskStatusSuccess {
			l.recordAccountTaskSuccess(task)
		}
	} else if err := l.taskRepo.Update(ctx, task); err != nil {
		l.logger.Error("Failed to update task", zap.Error(err))
		return
	}

	if shouldUploadOSS {
		l.uploadOSSAndCallback(task)
		return
	}

	if shouldCallback {
		l.fireCallback(task.CallbackURL, task)
	}
}

func failedTaskProgress(current, parsed int) int {
	if parsed > constants.MinTaskProgress {
		return parsed
	}
	return current
}

func parsedButtonsJSON(buttons []string, current *string) (string, bool) {
	if len(buttons) == 0 {
		return "", false
	}
	data, err := json.Marshal(buttons)
	if err != nil {
		return "", false
	}
	next := string(data)
	return next, current == nil || *current != next
}

func runeCount(value string) int {
	return len([]rune(value))
}

func (l *Listener) decrementAccountJobs(task *model.Task) {
	if task.AccountID == nil {
		return
	}
	if l.accountRepo == nil {
		l.logger.Warn("Account repository missing; cannot decrement account jobs",
			zap.String("task_id", task.TaskID),
			zap.Uint("account_id", *task.AccountID))
		return
	}
	ctx, cancel := listenerOperationContext()
	defer cancel()

	if err := l.accountRepo.DecrementJobs(ctx, *task.AccountID); err != nil {
		l.logger.Error("Failed to decrement account jobs",
			zap.String("task_id", task.TaskID),
			zap.Uint("account_id", *task.AccountID),
			zap.Error(err))
	}
}

func (l *Listener) recordAccountTaskFailure(task *model.Task) {
	if task.AccountID == nil || l.accountRepo == nil {
		return
	}
	lastError := strings.TrimSpace(task.ErrorMessage)
	if lastError == "" {
		lastError = "task failed"
	}
	ctx, cancel := listenerOperationContext()
	defer cancel()

	if err := l.accountRepo.RecordTaskResult(ctx, *task.AccountID, false, lastError); err != nil {
		l.logger.Error("Failed to record account task failure",
			zap.String("task_id", task.TaskID),
			zap.Uint("account_id", *task.AccountID),
			zap.Error(err))
	}
}

func (l *Listener) recordAccountTaskSuccess(task *model.Task) {
	if task.AccountID == nil || l.accountRepo == nil {
		return
	}

	ctx, cancel := listenerOperationContext()
	defer cancel()

	if err := l.accountRepo.RecordTaskResult(ctx, *task.AccountID, true, ""); err != nil {
		l.logger.Error("Failed to record account task success",
			zap.String("task_id", task.TaskID),
			zap.Uint("account_id", *task.AccountID),
			zap.Error(err))
	}
}

func (l *Listener) uploadOSSAndCallback(task *model.Task) {
	if l == nil || task == nil || l.taskRepo == nil || l.ossUploader == nil {
		return
	}
	if task.Status != model.TaskStatusSuccess || task.TaskID == "" || task.ImageURL == "" {
		return
	}

	fallbackTask := *task
	go func() {
		taskID := fallbackTask.TaskID
		callbackURL := fallbackTask.CallbackURL
		imageURL := fallbackTask.ImageURL

		uploadCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		dbCtx, dbCancel := listenerOperationContext()
		latestTask, err := l.taskRepo.GetByTaskID(dbCtx, taskID)
		dbCancel()
		if err == nil && latestTask != nil && latestTask.OSSImageURL != "" {
			l.logger.Debug("OSS already uploaded, skipped", zap.String("task_id", taskID))
			l.fireCallback(callbackURL, latestTask)
			return
		}

		ossURL, err := l.ossUploader.UploadFromURL(uploadCtx, taskID, imageURL)
		if err != nil {
			l.logger.Error("OSS Upload failed",
				zap.String("task_id", taskID),
				zap.String("image_url", redact.URL(imageURL)),
				zap.Error(err),
			)
			l.fireCallback(callbackURL, taskCallbackFallback(latestTask, fallbackTask))
			return
		}

		dbCtx, dbCancel = listenerOperationContext()
		err = l.taskRepo.UpdateOSSImageURL(dbCtx, taskID, ossURL)
		dbCancel()
		if err != nil {
			l.logger.Error("OSS URL write to database failed",
				zap.String("task_id", taskID),
				zap.Error(err),
			)
			if latestTask != nil && latestTask.Status == model.TaskStatusSuccess {
				latestTask.OSSImageURL = ossURL
				l.fireCallback(callbackURL, latestTask)
			} else if latestTask == nil {
				fallbackTask.OSSImageURL = ossURL
				l.fireCallback(callbackURL, &fallbackTask)
			}
			return
		}

		dbCtx, dbCancel = listenerOperationContext()
		updatedTask, err := l.taskRepo.GetByTaskID(dbCtx, taskID)
		dbCancel()
		if err == nil && updatedTask != nil {
			l.fireCallback(callbackURL, updatedTask)
		}
	}()
}

func taskCallbackFallback(latestTask *model.Task, fallbackTask model.Task) *model.Task {
	if latestTask != nil {
		return latestTask
	}
	if fallbackTask.TaskID == "" {
		return nil
	}
	return &fallbackTask
}

func (l *Listener) markMessageMatched(msgID string) {
	if msgID == "" {
		return
	}

	l.msgMutex.Lock()
	defer l.msgMutex.Unlock()

	if l.matchedMsgIDs == nil {
		l.matchedMsgIDs = make(map[string]time.Time)
	}
	now := time.Now()
	for id, matchedAt := range l.matchedMsgIDs {
		if now.Sub(matchedAt) > matchedMessageTTL {
			delete(l.matchedMsgIDs, id)
		}
	}
	l.matchedMsgIDs[msgID] = now
}

// fireCallback sends a POST request to the callback URL with the task result.
// The request body matches the GET /api/v1/tasks/{task_id} response format.
func (l *Listener) fireCallback(callbackURL string, task *model.Task) {
	if callbackURL == "" || task == nil {
		return
	}
	go func() {
		l.sendCallbackWithRetry(context.Background(), callbackURL, task)
	}()
}

func (l *Listener) sendCallbackWithRetry(parent context.Context, callbackURL string, task *model.Task) {
	logCallbackURL := redact.URL(callbackURL)
	taskID := callbackTaskID(task)
	for attempt := 1; attempt <= constants.CallbackMaxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(parent, constants.CallbackTimeout)
		retry, err := l.sendCallback(ctx, callbackURL, task)
		cancel()

		if err == nil {
			return
		}

		if !retry || attempt == constants.CallbackMaxAttempts {
			l.logger.Error("Callback failed",
				zap.String("task_id", taskID),
				zap.String("callback_url", logCallbackURL),
				zap.Int("attempt", attempt),
				zap.String("error", redact.Text(err.Error())))
			return
		}

		delay := time.Duration(attempt) * constants.CallbackRetryBaseDelay
		l.logger.Warn("Callback failed, preparing to retry",
			zap.String("task_id", taskID),
			zap.String("callback_url", logCallbackURL),
			zap.Int("attempt", attempt),
			zap.Duration("delay", delay),
			zap.String("error", redact.Text(err.Error())))

		timer := time.NewTimer(delay)
		select {
		case <-parent.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func (l *Listener) sendCallback(ctx context.Context, callbackURL string, task *model.Task) (bool, error) {
	if task == nil {
		return false, fmt.Errorf("callback task is required")
	}
	normalizedURL, err := normalizeCallbackURL(callbackURL)
	if err != nil {
		return false, err
	}

	body := struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Data    *callbackTaskView `json:"data,omitempty"`
	}{
		Code:    "SUCCESS",
		Message: "success",
		Data:    callbackTaskViewFromModel(task),
	}
	data, err := json.Marshal(body)
	if err != nil {
		return false, fmt.Errorf("marshal callback body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, normalizedURL, bytes.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("create callback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.httpClient().Do(req)
	if err != nil {
		retry := !safehttp.IsPrivateOrLocalAddressError(err)
		return retry, fmt.Errorf("send callback request: %s", redact.Text(err.Error()))
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		retry := resp.StatusCode >= http.StatusInternalServerError
		return retry, fmt.Errorf("callback returned status %d", resp.StatusCode)
	}

	l.logger.Info("Callback sent",
		zap.String("task_id", task.TaskID),
		zap.String("callback_url", redact.URL(normalizedURL)),
		zap.Int("status_code", resp.StatusCode))
	return false, nil
}

func callbackTaskID(task *model.Task) string {
	if task == nil {
		return ""
	}
	return task.TaskID
}

type callbackTaskView struct {
	TaskID       string           `json:"task_id"`
	ParentTaskID string           `json:"parent_task_id,omitempty"`
	Type         model.TaskType   `json:"type"`
	Prompt       string           `json:"prompt,omitempty"`
	Status       model.TaskStatus `json:"status"`
	Progress     int              `json:"progress"`
	ImageURL     string           `json:"image_url,omitempty"`
	OSSImageURL  string           `json:"oss_image_url,omitempty"`
	ErrorMessage string           `json:"error_message,omitempty"`
	Description  string           `json:"description,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	FinishedAt   *time.Time       `json:"finished_at,omitempty"`
}

func callbackTaskViewFromModel(task *model.Task) *callbackTaskView {
	if task == nil {
		return nil
	}
	return &callbackTaskView{
		TaskID:       task.TaskID,
		ParentTaskID: task.ParentTaskID,
		Type:         task.Type,
		Prompt:       task.Prompt,
		Status:       task.Status,
		Progress:     task.Progress,
		ImageURL:     task.ImageURL,
		OSSImageURL:  task.OSSImageURL,
		ErrorMessage: redact.Text(task.ErrorMessage),
		Description:  task.Description,
		CreatedAt:    task.CreatedAt,
		UpdatedAt:    task.UpdatedAt,
		FinishedAt:   task.FinishedAt,
	}
}

func (l *Listener) httpClient() *http.Client {
	if l != nil && l.callbackClient != nil {
		return l.callbackClient
	}
	return defaultCallbackHTTPClient
}

// matchDescribeTask finds the pending describe task for the channel and updates it with descriptions
func (l *Listener) matchDescribeTask(channelID string, parsed *ParsedMessage) {
	if l == nil || l.taskRepo == nil || l.accountRepo == nil || parsed == nil {
		return
	}
	channelID = strings.TrimSpace(channelID)
	parsed.MessageID = strings.TrimSpace(parsed.MessageID)
	if channelID == "" || parsed.MessageID == "" {
		return
	}
	log := l.logger
	if log == nil {
		log = zap.NewNop()
	}

	ctx, cancel := listenerOperationContext()
	defer cancel()

	tasks, err := l.taskRepo.GetDiscordActiveTasks(ctx, 50)
	if err != nil {
		log.Error("Failed to find describe tasks", zap.Error(err))
		return
	}

	for _, task := range tasks {
		if task == nil {
			continue
		}

		if task.Type != model.TaskTypeDescribe {
			continue
		}
		if model.IsTerminalTaskStatus(task.Status) {
			continue
		}
		if task.AccountID == nil {
			continue
		}

		account, err := l.accountRepo.GetByID(ctx, *task.AccountID)
		if err != nil || account == nil {
			continue
		}
		if account.ChannelID != channelID {
			continue
		}

		log.Info("[Match describe task]",
			zap.String("task_id", task.TaskID),
			zap.String("channel_id", channelID))

		task.DiscordMessageID = parsed.MessageID
		task.Status = model.TaskStatusSuccess
		task.Description = parsed.Descriptions
		task.Progress = 100
		now := time.Now()
		task.FinishedAt = &now

		transitioned, err := l.taskRepo.UpdateTerminal(ctx, task)
		if err != nil {
			log.Error("Failed to update describe task", zap.Error(err))
			return
		}
		if !transitioned {
			log.Debug("[Describe terminal update skipped]", zap.String("task_id", task.TaskID))
			return
		}

		l.decrementAccountJobs(task)
		l.recordAccountTaskSuccess(task)
		l.fireCallback(task.CallbackURL, task)
		l.markMessageMatched(parsed.MessageID)
		log.Info("[Describe completed]",
			zap.String("task_id", task.TaskID),
			zap.String("message_id", parsed.MessageID))
		return
	}

	log.Debug("[No matching describe task found]", zap.String("channel_id", channelID))
}
