package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/internal/discord"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/repository"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"go.uber.org/zap"
)

type TaskService interface {
	CreateImagineTask(ctx context.Context, req *CreateTaskRequest) (*TaskResponse, error)
	CreateDescribeTask(ctx context.Context, req *CreateDescribeTaskRequest) (*TaskResponse, error)
	GetTask(ctx context.Context, taskID string) (*model.Task, error)
	ListTasks(ctx context.Context, userID uint, limit, offset int) ([]model.Task, int64, error)
	ProcessTask(ctx context.Context, msg *TaskMessage) error
	ProcessDescribeTask(ctx context.Context, msg *TaskDescribeMessage) error
	GetQueueList(ctx context.Context) (*QueueStatus, error)
	PerformTaskAction(ctx context.Context, req *TaskActionRequest) (*TaskResponse, error)
	ProcessActionTask(ctx context.Context, msg *TaskActionMessage) error
}

type taskService struct {
	taskRepo       repository.TaskRepository
	accountService AccountService
	discord        *discord.Client
	redis          *redis.Client
	taskConfig     *config.TaskConfig
	logger         *zap.Logger
}

func NewTaskService(
	taskRepo repository.TaskRepository,
	accountService AccountService,
	discordConfig *config.DiscordConfig,
	redisClient *redis.Client,
	taskConfig *config.TaskConfig,
	logger *zap.Logger,
) TaskService {
	return &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        discord.NewClient(discordConfig),
		redis:          redisClient,
		taskConfig:     taskConfig,
		logger:         logger,
	}
}

type CreateTaskRequest struct {
	UserID      uint
	Prompt      string
	CallbackURL string
}

type TaskResponse struct {
	TaskID string           `json:"task_id"`
	Status model.TaskStatus `json:"status"`
}

func (s *taskService) CreateImagineTask(ctx context.Context, req *CreateTaskRequest) (*TaskResponse, error) {
	account, err := s.accountService.GetAvailableAccount(ctx)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.ErrCodeAccountUnavailable, "no available account", err)
	}

	if account == nil {
		return nil, apperrors.NewAccountUnavailable("all accounts are busy or unhealthy")
	}

	taskID := generateTaskID()
	task := &model.Task{
		TaskID:      taskID,
		UserID:      req.UserID,
		AccountID:   &account.ID,
		Type:        model.TaskTypeImagine,
		Prompt:      req.Prompt,
		Status:      model.TaskStatusPending,
		CallbackURL: req.CallbackURL,
	}

	if err := s.taskRepo.Create(ctx, task); err != nil {
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to create task", err)
	}

	if err := s.accountService.IncrementJobs(ctx, account.ID); err != nil {
		s.logger.Error("Failed to increment account job count, cleaning up task",
			zap.String("task_id", taskID),
			zap.Uint("account_id", account.ID),
			zap.Error(err))
		if delErr := s.taskRepo.UpdateStatus(ctx, taskID, model.TaskStatusFailed); delErr != nil {
			s.logger.Warn("Failed to delete failed task", zap.Error(delErr))
		}
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to increment job count", err)
	}

	if err := s.enqueueTask(ctx, task, account); err != nil {
		s.logger.Error("Failed to enqueue task, rolling back",
			zap.String("task_id", taskID),
			zap.Uint("account_id", account.ID),
			zap.Error(err))

		rollbackCtx := context.Background()
		if decErr := s.accountService.DecrementJobs(rollbackCtx, account.ID); decErr != nil {
			s.logger.Error("Failed to decrement account job count",
				zap.Uint("account_id", account.ID),
				zap.Error(decErr))
		}

		if updateErr := s.taskRepo.UpdateStatus(rollbackCtx, taskID, model.TaskStatusFailed); updateErr != nil {
			s.logger.Warn("Failed to update task status", zap.Error(updateErr))
		}

		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to enqueue task", err)
	}

	return &TaskResponse{
		TaskID: taskID,
		Status: model.TaskStatusPending,
	}, nil
}

func (s *taskService) GetTask(ctx context.Context, taskID string) (*model.Task, error) {
	return s.taskRepo.GetByTaskID(ctx, taskID)
}

func (s *taskService) ListTasks(ctx context.Context, userID uint, limit, offset int) ([]model.Task, int64, error) {
	return s.taskRepo.List(ctx, userID, limit, offset)
}

type TaskMessage struct {
	TaskID      string `json:"task_id"`
	Prompt      string `json:"prompt"`
	GuildID     string `json:"guild_id"`
	ChannelID   string `json:"channel_id"`
	UserToken   string `json:"user_token"`
	AccountID   uint   `json:"account_id"`
	CallbackURL string `json:"callback_url"`
}

func (s *taskService) enqueueTask(ctx context.Context, task *model.Task, account *model.Account) error {
	msg := TaskMessage{
		TaskID:      task.TaskID,
		Prompt:      task.Prompt,
		GuildID:     account.GuildID,
		ChannelID:   account.ChannelID,
		UserToken:   account.UserToken,
		AccountID:   account.ID,
		CallbackURL: task.CallbackURL,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return s.redis.LPush(ctx, s.taskConfig.QueueName, data).Err()
}

func (s *taskService) withRetry(taskID string, fn func() error) error {
	var lastErr error
	maxRetries := s.taskConfig.MaxRetries
	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			break
		}
		if attempt < maxRetries {
			s.logger.Warn("Discord API call failed, preparing to retry",
				zap.String("task_id", taskID),
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", maxRetries),
				zap.Error(lastErr))
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
		}
	}
	return lastErr
}

func (s *taskService) ProcessTask(ctx context.Context, msg *TaskMessage) error {
	if err := s.taskRepo.UpdateStatus(ctx, msg.TaskID, model.TaskStatusProcessing); err != nil {
		return err
	}

	discordReq := &discord.ImagineRequest{
		Prompt:    msg.Prompt,
		GuildID:   msg.GuildID,
		ChannelID: msg.ChannelID,
		UserToken: msg.UserToken,
	}

	if lastErr := s.withRetry(msg.TaskID, func() error {
		return s.discord.Imagine(discordReq)
	}); lastErr != nil {
		s.handleDiscordCallFailure(ctx, msg.TaskID, msg.AccountID, lastErr)
		return lastErr
	}

	// Update status to submitted (waiting for Discord callback)
	if err := s.taskRepo.UpdateStatus(ctx, msg.TaskID, model.TaskStatusSubmitted); err != nil {
		return err
	}

	// Record task success result to account
	s.accountService.RecordTaskResult(ctx, msg.AccountID, true, "")

	return nil
}

type QueueStatus struct {
	WaitingInQueue  []TaskMessage `json:"waiting_in_queue"`
	ProcessingTasks []*model.Task `json:"processing_tasks"`
	QueueLength     int64         `json:"queue_length"`
	ProcessingCount int           `json:"processing_count"`
}

func (s *taskService) GetQueueList(ctx context.Context) (*QueueStatus, error) {
	queueName := s.taskConfig.QueueName

	length := s.redis.LLen(ctx, queueName).Val()
	var waitingTasks []TaskMessage

	if length > 0 {
		results, err := s.redis.LRange(ctx, queueName, 0, length-1).Result()
		if err != nil {
			return nil, err
		}

		for _, result := range results {
			var taskMsg TaskMessage
			if err := json.Unmarshal([]byte(result), &taskMsg); err != nil {
				continue
			}
			waitingTasks = append(waitingTasks, taskMsg)
		}
	}

	processingTasks, err := s.taskRepo.GetPendingTasks(ctx, 100)
	if err != nil {
		return nil, err
	}

	return &QueueStatus{
		WaitingInQueue:  waitingTasks,
		ProcessingTasks: processingTasks,
		QueueLength:     length,
		ProcessingCount: len(processingTasks),
	}, nil
}

func generateTaskID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

// ===== Describe Task =====

type CreateDescribeTaskRequest struct {
	UserID      uint
	ImageURL    string
	CallbackURL string
}

type TaskDescribeMessage struct {
	TaskID      string `json:"task_id"`
	ImageURL    string `json:"image_url"`
	GuildID     string `json:"guild_id"`
	ChannelID   string `json:"channel_id"`
	UserToken   string `json:"user_token"`
	AccountID   uint   `json:"account_id"`
	CallbackURL string `json:"callback_url"`
}

func (s *taskService) CreateDescribeTask(ctx context.Context, req *CreateDescribeTaskRequest) (*TaskResponse, error) {
	account, err := s.accountService.GetAvailableAccount(ctx)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.ErrCodeAccountUnavailable, "no available account", err)
	}
	if account == nil {
		return nil, apperrors.NewAccountUnavailable("all accounts are busy or unhealthy")
	}

	taskID := generateTaskID()
	task := &model.Task{
		TaskID:      taskID,
		UserID:      req.UserID,
		AccountID:   &account.ID,
		Type:        model.TaskTypeDescribe,
		Prompt:      req.ImageURL,
		Status:      model.TaskStatusPending,
		CallbackURL: req.CallbackURL,
	}

	if err := s.taskRepo.Create(ctx, task); err != nil {
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to create task", err)
	}

	if err := s.accountService.IncrementJobs(ctx, account.ID); err != nil {
		s.logger.Error("Failed to increment account job count, cleaning up task",
			zap.String("task_id", taskID),
			zap.Uint("account_id", account.ID),
			zap.Error(err))
		if delErr := s.taskRepo.UpdateStatus(ctx, taskID, model.TaskStatusFailed); delErr != nil {
			s.logger.Warn("Failed to delete failed task", zap.Error(delErr))
		}
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to increment job count", err)
	}

	if err := s.enqueueDescribeTask(ctx, task, account); err != nil {
		s.logger.Error("Failed to enqueue describe task, rolling back",
			zap.String("task_id", taskID),
			zap.Uint("account_id", account.ID),
			zap.Error(err))

		rollbackCtx := context.Background()
		if decErr := s.accountService.DecrementJobs(rollbackCtx, account.ID); decErr != nil {
			s.logger.Error("Failed to decrement account job count",
				zap.Uint("account_id", account.ID),
				zap.Error(decErr))
		}
		if updateErr := s.taskRepo.UpdateStatus(rollbackCtx, taskID, model.TaskStatusFailed); updateErr != nil {
			s.logger.Warn("Failed to update task status", zap.Error(updateErr))
		}
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to enqueue describe task", err)
	}

	return &TaskResponse{
		TaskID: taskID,
		Status: model.TaskStatusPending,
	}, nil
}

func (s *taskService) enqueueDescribeTask(ctx context.Context, task *model.Task, account *model.Account) error {
	msg := TaskDescribeMessage{
		TaskID:      task.TaskID,
		ImageURL:    task.Prompt,
		GuildID:     account.GuildID,
		ChannelID:   account.ChannelID,
		UserToken:   account.UserToken,
		AccountID:   account.ID,
		CallbackURL: task.CallbackURL,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.redis.LPush(ctx, s.taskConfig.QueueName, data).Err()
}

func (s *taskService) ProcessDescribeTask(ctx context.Context, msg *TaskDescribeMessage) error {
	if err := s.taskRepo.UpdateStatus(ctx, msg.TaskID, model.TaskStatusProcessing); err != nil {
		return err
	}

	describeReq := &discord.DescribeRequest{
		ImageURL:  msg.ImageURL,
		GuildID:   msg.GuildID,
		ChannelID: msg.ChannelID,
		UserToken: msg.UserToken,
	}

	if lastErr := s.withRetry(msg.TaskID, func() error {
		return s.discord.Describe(describeReq)
	}); lastErr != nil {
		s.handleDiscordCallFailure(ctx, msg.TaskID, msg.AccountID, lastErr)
		return lastErr
	}

	if err := s.taskRepo.UpdateStatus(ctx, msg.TaskID, model.TaskStatusSubmitted); err != nil {
		return err
	}

	s.accountService.RecordTaskResult(ctx, msg.AccountID, true, "")
	return nil
}

type TaskActionRequest struct {
	TaskID      string `json:"task_id" binding:"required"`           // Original task ID
	ActionType  string `json:"action_type" binding:"required"`       // Operation type: upscale, zoom_out_2x, zoom_out_1_5x, upscale_subtle, upscale_creative
	Index       int    `json:"index" binding:"required,min=1,max=4"` // Index: 1-4, representing the position of the image to be operated on
	CallbackURL string `json:"callback_url"`
}

// PerformTaskAction
func (s *taskService) PerformTaskAction(ctx context.Context, req *TaskActionRequest) (*TaskResponse, error) {
	parentTask, err := s.taskRepo.GetByTaskID(ctx, req.TaskID)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskNotFound, "parent task not found", err)
	}

	if parentTask.Status != model.TaskStatusSuccess {
		return nil, apperrors.New(apperrors.ErrCodeTaskNotCompleted,
			fmt.Sprintf("parent task is not completed yet, current status: %s", parentTask.Status))
	}

	if parentTask.DiscordMessageID == "" {
		return nil, apperrors.New(apperrors.ErrCodeInvalidInput, "parent task has no discord message ID")
	}

	if parentTask.Buttons == nil || *parentTask.Buttons == "" {
		return nil, apperrors.New(apperrors.ErrCodeInvalidInput, "parent task has no buttons")
	}

	customID, taskType, err := getCustomIDFromButtonsDynamic(*parentTask.Buttons, req.ActionType, req.Index)
	if err != nil {
		return nil, err
	}

	if parentTask.AccountID == nil {
		return nil, apperrors.New(apperrors.ErrCodeInvalidInput, "parent task has no associated account")
	}

	account, err := s.accountService.GetAccountByID(ctx, *parentTask.AccountID)
	if err != nil {
		return nil, apperrors.Wrap(apperrors.ErrCodeAccountNotFound, "failed to get account", err)
	}

	// 7. Create new task record
	newTaskID := generateTaskID()
	newTask := &model.Task{
		TaskID:       newTaskID,
		UserID:       parentTask.UserID,
		AccountID:    parentTask.AccountID,
		ParentTaskID: req.TaskID,
		Type:         taskType,
		Prompt:       parentTask.Prompt,
		Status:       model.TaskStatusPending,
		CallbackURL:  req.CallbackURL,
	}

	if err := s.taskRepo.Create(ctx, newTask); err != nil {
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to create task", err)
	}

	if err := s.accountService.IncrementJobs(ctx, account.ID); err != nil {
		s.logger.Error("Failed to increment job count for account, cleaning up task",
			zap.String("task_id", newTaskID),
			zap.Uint("account_id", account.ID),
			zap.Error(err))
		if delErr := s.taskRepo.UpdateStatus(ctx, newTaskID, model.TaskStatusFailed); delErr != nil {
			s.logger.Warn("Failed to delete failed task", zap.Error(delErr))
		}
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to increment job count", err)
	}

	actionMsg := &TaskActionMessage{
		TaskID:           newTaskID,
		ParentTaskID:     req.TaskID,
		CustomID:         customID,
		DiscordMessageID: parentTask.DiscordMessageID,
		GuildID:          account.GuildID,
		ChannelID:        account.ChannelID,
		UserToken:        account.UserToken,
		AccountID:        account.ID,
		CallbackURL:      req.CallbackURL,
	}

	if err := s.enqueueActionTask(ctx, actionMsg); err != nil {
		s.logger.Error("Failed to enqueue action task, rolling back",
			zap.String("task_id", newTaskID),
			zap.Uint("account_id", account.ID),
			zap.Error(err))

		rollbackCtx := context.Background()
		if decErr := s.accountService.DecrementJobs(rollbackCtx, account.ID); decErr != nil {
			s.logger.Error("Failed to decrement job count for account during rollback",
				zap.Uint("account_id", account.ID),
				zap.Error(decErr))
		}

		if updateErr := s.taskRepo.UpdateStatus(rollbackCtx, newTaskID, model.TaskStatusFailed); updateErr != nil {
			s.logger.Warn("Failed to update task status", zap.Error(updateErr))
		}

		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to enqueue action task", err)
	}

	return &TaskResponse{
		TaskID: newTaskID,
		Status: model.TaskStatusPending,
	}, nil
}

type TaskActionMessage struct {
	TaskID           string `json:"task_id"`
	ParentTaskID     string `json:"parent_task_id"`
	CustomID         string `json:"custom_id"`
	DiscordMessageID string `json:"discord_message_id"`
	GuildID          string `json:"guild_id"`
	ChannelID        string `json:"channel_id"`
	UserToken        string `json:"user_token"`
	AccountID        uint   `json:"account_id"`
	CallbackURL      string `json:"callback_url"`
}

func (s *taskService) enqueueActionTask(ctx context.Context, msg *TaskActionMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return s.redis.LPush(ctx, s.taskConfig.QueueName, data).Err()
}

func (s *taskService) ProcessActionTask(ctx context.Context, msg *TaskActionMessage) error {
	// 1. Update status to processing
	if err := s.taskRepo.UpdateStatus(ctx, msg.TaskID, model.TaskStatusProcessing); err != nil {
		return err
	}

	// 2. Call Discord API to perform button action, with retries
	discordReq := &discord.ButtonActionRequest{
		CustomID:  msg.CustomID,
		MessageID: msg.DiscordMessageID,
		GuildID:   msg.GuildID,
		ChannelID: msg.ChannelID,
		UserToken: msg.UserToken,
	}

	if lastErr := s.withRetry(msg.TaskID, func() error {
		return s.discord.PerformButtonAction(discordReq)
	}); lastErr != nil {
		s.handleDiscordCallFailure(ctx, msg.TaskID, msg.AccountID, lastErr)
		return lastErr
	}

	// 3. Update status to submitted (waiting for Discord callback)
	if err := s.taskRepo.UpdateStatus(ctx, msg.TaskID, model.TaskStatusSubmitted); err != nil {
		return err
	}

	// Record task success result to account
	s.accountService.RecordTaskResult(ctx, msg.AccountID, true, "")

	return nil
}

func (s *taskService) handleDiscordCallFailure(ctx context.Context, taskID string, accountID uint, callErr error) {
	task, _ := s.taskRepo.GetByTaskID(ctx, taskID)
	if task != nil {
		task.Status = model.TaskStatusFailed
		task.ErrorMessage = callErr.Error()
		now := time.Now()
		task.FinishedAt = &now
		s.taskRepo.Update(ctx, task)
	}
	s.accountService.DecrementJobs(ctx, accountID)
	s.accountService.RecordTaskResult(ctx, accountID, false, callErr.Error())
}

func getCustomIDFromButtonsDynamic(buttonsJSON string, actionType string, index int) (string, model.TaskType, error) {
	var buttons []string
	if err := json.Unmarshal([]byte(buttonsJSON), &buttons); err != nil {
		return "", "", fmt.Errorf("failed to parse buttons: %w", err)
	}

	if len(buttons) == 0 {
		return "", "", fmt.Errorf("buttons array is empty")
	}

	for _, customID := range buttons {
		if customID == "" || customID == "\u200b" || customID == "Web" {
			continue
		}

		if matchCustomID(customID, actionType, index) {
			taskType := inferTaskType(actionType)
			return customID, taskType, nil
		}
	}

	for _, customID := range buttons {
		if customID == "" || customID == "\u200b" || customID == "Web" {
			continue
		}

		if matchCustomIDIgnoreIndex(customID, actionType) {
			taskType := inferTaskType(actionType)
			return customID, taskType, nil
		}
	}

	availableActions := extractAvailableActions(buttons)
	return "", "", fmt.Errorf("action_type '%s' not found in buttons. Available actions: %v",
		actionType, availableActions)
}

func matchCustomID(customID, actionType string, index int) bool {
	// Upscale: MJ::JOB::upsample::1::uuid
	if actionType == "upscale" {
		return strings.Contains(customID, "MJ::JOB::upsample") &&
			!strings.Contains(customID, "subtle") &&
			!strings.Contains(customID, "creative") &&
			strings.Contains(customID, fmt.Sprintf("::%d::", index))
	}

	// Zoom Out 2x: MJ::Outpaint::50::1::uuid::SOLO
	if actionType == "zoom_out_2x" {
		return strings.Contains(customID, "MJ::Outpaint::50::") && strings.Contains(customID, fmt.Sprintf("::%d::", index))
	}

	// Zoom Out 1.5x: MJ::Outpaint::75::1::uuid::SOLO
	if actionType == "zoom_out_1_5x" {
		return strings.Contains(customID, "MJ::Outpaint::75::") && strings.Contains(customID, fmt.Sprintf("::%d::", index))
	}

	// Upscale (Subtle): MJ::JOB::upsample_v6r1_2x_subtle::1::uuid::SOLO or MJ::JOB::upsample_v7_2x_subtle::1::uuid::SOLO
	if actionType == "upscale_subtle" {
		return strings.Contains(customID, "upsample") &&
			strings.Contains(customID, "subtle") &&
			strings.Contains(customID, fmt.Sprintf("::%d::", index))
	}

	// Upscale (Creative): MJ::JOB::upsample_v6r1_2x_creative::1::uuid::SOLO or MJ::JOB::upsample_v7_2x_creative::1::uuid::SOLO
	if actionType == "upscale_creative" {
		return strings.Contains(customID, "upsample") &&
			strings.Contains(customID, "creative") &&
			strings.Contains(customID, fmt.Sprintf("::%d::", index))
	}

	return false
}

func matchCustomIDIgnoreIndex(customID, actionType string) bool {
	// Upscale: MJ::JOB::upsample::1::uuid
	if actionType == "upscale" {
		return strings.Contains(customID, "MJ::JOB::upsample") &&
			!strings.Contains(customID, "subtle") &&
			!strings.Contains(customID, "creative")
	}

	// Zoom Out 2x: MJ::Outpaint::50::1::uuid::SOLO
	if actionType == "zoom_out_2x" {
		return strings.Contains(customID, "MJ::Outpaint::50::")
	}

	// Zoom Out 1.5x: MJ::Outpaint::75::1::uuid::SOLO
	if actionType == "zoom_out_1_5x" {
		return strings.Contains(customID, "MJ::Outpaint::75::")
	}

	// Upscale (Subtle): MJ::JOB::upsample_v6r1_2x_subtle::1::uuid::SOLO or MJ::JOB::upsample_v7_2x_subtle::1::uuid::SOLO
	if actionType == "upscale_subtle" {
		return strings.Contains(customID, "upsample") && strings.Contains(customID, "subtle")
	}

	// Upscale (Creative): MJ::JOB::upsample_v6r1_2x_creative::1::uuid::SOLO or MJ::JOB::upsample_v7_2x_creative::1::uuid::SOLO
	if actionType == "upscale_creative" {
		return strings.Contains(customID, "upsample") && strings.Contains(customID, "creative")
	}

	return false
}

func inferTaskType(actionType string) model.TaskType {
	switch actionType {
	case "upscale":
		return model.TaskTypeUpscale
	case "zoom_out_2x":
		return model.TaskTypeZoomOut2x
	case "zoom_out_1_5x":
		return model.TaskTypeZoomOut1_5x
	case "upscale_subtle":
		return model.TaskTypeUpscaleSubtle
	case "upscale_creative":
		return model.TaskTypeUpscaleCreative
	default:
		return model.TaskTypeImagine // default task type
	}
}

func extractAvailableActions(buttons []string) []string {
	var actions []string
	for _, btn := range buttons {
		if btn == "" || btn == "\u200b" || btn == "Web" {
			continue
		}
		actions = append(actions, btn)
	}
	return actions
}
