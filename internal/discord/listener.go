package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/oss"
	"github.com/trae/midjourney-api/internal/repository"
	"github.com/trae/midjourney-api/pkg/constants"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Use discordgo for Discord events
type Listener struct {
	session         *discordgo.Session
	parser          *MessageParser
	taskRepo        repository.TaskRepository
	accountRepo     repository.AccountRepository
	ossUploader     oss.Uploader
	logger          *zap.Logger
	midjourneyBotID string
	matchedMsgIDs   map[string]bool
	msgMutex        sync.RWMutex
}

func NewListener(botToken, midjourneyBotID string, db *gorm.DB, logger *zap.Logger, ossUploader oss.Uploader) *Listener {
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
		logger:          logger,
		midjourneyBotID: midjourneyBotID,
		matchedMsgIDs:   make(map[string]bool),
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
	// Open WebSocket connection
	err := l.session.Open()
	if err != nil {
		l.logger.Error("Failed to open Discord connection", zap.Error(err))
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
	l.logger.Info("Stopping Discord listener...")
	return l.session.Close()
}

func (l *Listener) handleMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID != l.midjourneyBotID {
		return
	}

	parsed := l.parser.ParseDiscordMessage(m.Message)
	if parsed == nil {
		return
	}

	task, err := l.taskRepo.GetByDiscordMessageID(context.Background(), m.ID)
	if err == nil && task != nil {
		l.updateTask(task, parsed)
		return
	}

	l.updateMatchingTask(parsed, m.Message)
}

func (l *Listener) handleMessageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if m.Author == nil || m.Author.ID != l.midjourneyBotID {
		return
	}

	parsed := l.parser.ParseDiscordMessage(m.Message)
	if parsed == nil {
		return
	}

	task, err := l.taskRepo.GetByDiscordMessageID(context.Background(), m.ID)
	if err == nil && task != nil {
		l.updateTask(task, parsed)
		return
	}

	l.updateMatchingTask(parsed, m.Message)
}

func (l *Listener) isMessageMatched(msgID string) bool {
	l.msgMutex.RLock()
	defer l.msgMutex.RUnlock()
	return l.matchedMsgIDs[msgID]
}

func (l *Listener) updateMatchingTask(parsed *ParsedMessage, msg *discordgo.Message) {
	if l.isMessageMatched(parsed.MessageID) {
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

	tasks, err := l.taskRepo.GetPendingTasks(context.Background(), 50)
	if err != nil {
		l.logger.Error("Failed to find tasks", zap.Error(err))
		return
	}

	for _, task := range tasks {
		if time.Since(task.CreatedAt) > constants.TaskMatchTimeWindow {
			continue
		}

		if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailed {
			continue
		}

		if task.AccountID != nil {
			account, err := l.accountRepo.GetByID(context.Background(), *task.AccountID)
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
						zap.String("Task ID", task.TaskID),
						zap.String("Discord parsed Prompt", parsed.TaskPrompt),
						zap.String("Database task Prompt", task.Prompt),
					)
				} else {
					l.matchAndUpdateTask(task, parsed)
					return
				}
			}

			// Situation 2: action sub-task matching (upscale, zoom_out etc.)
			if task.ParentTaskID != "" {
				parentTask, err := l.taskRepo.GetByTaskID(context.Background(), task.ParentTaskID)
				if err == nil && parentTask != nil && parentTask.DiscordMessageID != "" {
					if l.parser.MatchTaskByPrompt(parsed.TaskPrompt, parentTask.Prompt) {
						l.logger.Info("[Match action sub-task - via Prompt] Task ID=" + task.TaskID + " Parent Task ID=" + task.ParentTaskID + " Type=" + string(task.Type))
						l.matchAndUpdateTask(task, parsed)
						return
					}
				}
			}
		}
	}

	l.logger.Debug("[No matching task found] Message ID=" + msg.ID + " Prompt=" + parsed.TaskPrompt + " Channel ID=" + msg.ChannelID)
}

func (l *Listener) matchActionTaskByReference(parentMessageID string, parsed *ParsedMessage) bool {
	parentTask, err := l.taskRepo.GetByDiscordMessageID(context.Background(), parentMessageID)
	if err != nil || parentTask == nil {
		return false
	}

	tasks, err := l.taskRepo.GetPendingTasks(context.Background(), 50)
	if err != nil {
		return false
	}

	for _, task := range tasks {
		if task.ParentTaskID == parentTask.TaskID {
			if time.Since(task.CreatedAt) > constants.ActionTaskMatchTimeWindow {
				continue
			}

			if task.DiscordMessageID != "" && task.DiscordMessageID != parsed.MessageID {
				continue
			}

			if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailed {
				continue
			}

			if task.DiscordMessageID == "" {
				l.logger.Info("[First match Action task] Task ID=" + task.TaskID + " Parent Task ID=" + task.ParentTaskID + " Type=" + string(task.Type) + " Progress=" + fmt.Sprintf("%d%%", parsed.Progress))
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
		if err := l.taskRepo.Update(context.Background(), task); err != nil {
			l.logger.Error("Failed to update task", zap.Error(err))
			return
		}
		if oldMessageID != "" {
			l.logger.Info("[Update message ID] Task ID=" + task.TaskID + " Old Message ID=" + oldMessageID + " New Message ID=" + parsed.MessageID)
		}
		l.markMessageMatched(parsed.MessageID)
	}

	l.updateTask(task, parsed)
}

func (l *Listener) updateTask(task *model.Task, parsed *ParsedMessage) {
	var guildID, channelID string
	if task.AccountID != nil {
		account, err := l.accountRepo.GetByID(context.Background(), *task.AccountID)
		if err == nil {
			guildID = account.GuildID
			channelID = account.ChannelID
		}
	}

	oldProgress := task.Progress
	task.Progress = parsed.Progress

	switch parsed.Status {
	case "pending":
		if task.Status == model.TaskStatusSubmitted {
			task.Status = model.TaskStatusInQueue
			l.logger.Info("[In queue]",
				zap.String("Task ID", task.TaskID),
				zap.String("MID", parsed.MessageID),
				zap.String("Guild ID", guildID),
				zap.String("Channel ID", channelID),
			)
		}
	case "processing":
		task.Status = model.TaskStatusProcessing
		if oldProgress != parsed.Progress {
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
		task.ImageURL = parsed.ImageURL
		now := time.Now()
		task.FinishedAt = &now
		l.logger.Info("[Completed]",
			zap.String("Task ID", task.TaskID),
			zap.String("MID", parsed.MessageID),
			zap.String("Guild ID", guildID),
			zap.String("Channel ID", channelID),
			zap.String("Image URL", parsed.ImageURL),
		)

		if l.ossUploader != nil && parsed.ImageURL != "" {
			taskID := task.TaskID
			imageURL := parsed.ImageURL
			go func() {
				uploadCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()

				latestTask, err := l.taskRepo.GetByTaskID(context.Background(), taskID)
				if err == nil && latestTask != nil && latestTask.OSSImageURL != "" {
					l.logger.Debug("OSS already uploaded, skipped", zap.String("task_id", taskID))
					return
				}

				ossURL, err := l.ossUploader.UploadFromURL(uploadCtx, taskID, imageURL)
				if err != nil {
					l.logger.Error("OSS Upload failed",
						zap.String("task_id", taskID),
						zap.String("image_url", imageURL),
						zap.Error(err),
					)
					return
				}
				if err := l.taskRepo.UpdateOSSImageURL(context.Background(), taskID, ossURL); err != nil {
					l.logger.Error("OSS URL write to database failed",
						zap.String("task_id", taskID),
						zap.Error(err),
					)
				}
			}()
		}

		if task.AccountID != nil {
			l.accountRepo.DecrementJobs(context.Background(), *task.AccountID)
		}
	case "failed":
		task.Status = model.TaskStatusFailed
		now := time.Now()
		task.FinishedAt = &now
		l.logger.Error("[Failed]",
			zap.String("Task ID", task.TaskID),
			zap.String("MID", parsed.MessageID),
			zap.String("Guild ID", guildID),
			zap.String("Channel ID", channelID),
		)

		if task.AccountID != nil {
			l.accountRepo.DecrementJobs(context.Background(), *task.AccountID)
		}
	}

	if len(parsed.Buttons) > 0 {
		buttonsJSON, _ := json.Marshal(parsed.Buttons)
		buttonsStr := string(buttonsJSON)
		task.Buttons = &buttonsStr
	}

	if err := l.taskRepo.Update(context.Background(), task); err != nil {
		l.logger.Error("Failed to update task", zap.Error(err))
	}
}

func (l *Listener) markMessageMatched(msgID string) {
	l.msgMutex.Lock()
	defer l.msgMutex.Unlock()
	l.matchedMsgIDs[msgID] = true
}
