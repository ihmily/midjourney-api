package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/internal/discord"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/repository"
	"github.com/trae/midjourney-api/internal/safehttp"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/redact"
	"go.uber.org/zap"
)

type TaskService interface {
	CreateImagineTask(ctx context.Context, req *CreateTaskRequest) (*TaskResponse, error)
	CreateDescribeTask(ctx context.Context, req *CreateDescribeTaskRequest) (*TaskResponse, error)
	GetTask(ctx context.Context, taskID string) (*model.Task, error)
	ListTasks(ctx context.Context, limit, offset int) ([]model.Task, int64, error)
	ProcessTask(ctx context.Context, msg *TaskMessage) error
	ProcessDescribeTask(ctx context.Context, msg *TaskDescribeMessage) error
	GetQueueList(ctx context.Context) (*QueueStatus, error)
	PerformTaskAction(ctx context.Context, req *TaskActionRequest) (*TaskResponse, error)
	ProcessActionTask(ctx context.Context, msg *TaskActionMessage) error
	RejectQueueMessage(taskID string, accountID uint, reason error) error
	SweepTimedOutTasks(ctx context.Context, cutoff time.Time, limit int) (int, error)
}

type taskService struct {
	taskRepo       repository.TaskRepository
	accountService AccountService
	discord        discordAPI
	redis          *redis.Client
	taskConfig     *config.TaskConfig
	logger         *zap.Logger
}

type discordAPI interface {
	Imagine(ctx context.Context, req *discord.ImagineRequest) error
	Describe(ctx context.Context, req *discord.DescribeRequest) error
	PerformButtonAction(ctx context.Context, req *discord.ButtonActionRequest) error
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
		logger:         taskLogger(logger),
	}
}

func taskLogger(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}
	return logger
}

func (s *taskService) log() *zap.Logger {
	if s == nil {
		return zap.NewNop()
	}
	return taskLogger(s.logger)
}

func (s *taskService) queueName() (string, error) {
	if s == nil || s.taskConfig == nil {
		return "", apperrors.New(apperrors.ErrCodeRedisError, "task config is required")
	}
	queueName := strings.TrimSpace(s.taskConfig.QueueName)
	if queueName == "" {
		return "", apperrors.New(apperrors.ErrCodeRedisError, "task queue_name is required")
	}
	return queueName, nil
}

func (s *taskService) maxRetries() int {
	if s == nil || s.taskConfig == nil || s.taskConfig.MaxRetries < 0 {
		return 0
	}
	return s.taskConfig.MaxRetries
}

func serviceDependencyError(name string) error {
	return apperrors.NewInternal("internal server error", fmt.Errorf("%s is required", name))
}

func (s *taskService) taskRepositoryOrError() (repository.TaskRepository, error) {
	if s == nil || s.taskRepo == nil {
		return nil, serviceDependencyError("task repository")
	}
	return s.taskRepo, nil
}

func (s *taskService) accountServiceOrError() (AccountService, error) {
	if s == nil || s.accountService == nil {
		return nil, serviceDependencyError("account service")
	}
	return s.accountService, nil
}

func (s *taskService) discordAPIOrError() (discordAPI, error) {
	if s == nil || s.discord == nil {
		return nil, serviceDependencyError("discord client")
	}
	return s.discord, nil
}

type CreateTaskRequest struct {
	Prompt      string
	CallbackURL string
}

type TaskResponse struct {
	TaskID string           `json:"task_id"`
	Status model.TaskStatus `json:"status"`
}

type QueueMessageKind string

const (
	QueueMessageKindImagine  QueueMessageKind = "imagine"
	QueueMessageKindDescribe QueueMessageKind = "describe"
	QueueMessageKindAction   QueueMessageKind = "action"
)

const taskRollbackTimeout = 10 * time.Second
const taskTimedOutMessage = "task timed out"

func preserveAppErrorOrWrap(code apperrors.ErrorCode, message string, err error) error {
	var appErr *apperrors.AppError
	if errors.As(err, &appErr) {
		return appErr
	}
	return apperrors.Wrap(code, message, err)
}

func requiredHTTPURL(field, value string) (string, error) {
	trimmed, err := requiredTrimmed(field, value)
	if err != nil {
		return "", err
	}
	if err := validateHTTPURLField(field, trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

func optionalHTTPURLTrimmedIfPresent(field, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed, err := optionalTrimmed(field, value)
	if err != nil {
		return "", err
	}
	if err := validateHTTPURLField(field, trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

func validateHTTPURLField(field, value string) error {
	if err := safehttp.ValidatePublicHTTPURL(value, field); err != nil {
		return apperrors.NewInvalidInput(err.Error())
	}
	return nil
}

func requiredTaskID(value string) (string, error) {
	return requiredLimitedTrimmed("task_id", value, constants.MaxTaskIDLength)
}

func requiredDiscordMessageID(value string) (string, error) {
	return requiredLimitedTrimmed("discord_message_id", value, constants.MaxDiscordMessageIDLength)
}

func requiredLimitedTrimmed(field, value string, maxLength int) (string, error) {
	trimmed, err := requiredTrimmed(field, value)
	if err != nil {
		return "", err
	}
	if maxLength > 0 && utf8.RuneCountInString(trimmed) > maxLength {
		return "", apperrors.NewInvalidInput(field + " must be at most " + strconv.Itoa(maxLength) + " characters")
	}
	return trimmed, nil
}

func (s *taskService) createQueuedTask(
	ctx context.Context,
	task *model.Task,
	account *model.Account,
	enqueue func(context.Context) error,
	operation string,
) (*TaskResponse, error) {
	if task == nil {
		return nil, apperrors.NewInvalidInput("task is required")
	}
	if account == nil {
		return nil, apperrors.NewInvalidInput("account is required")
	}
	if enqueue == nil {
		s.releaseAccountSlot(task.TaskID, account.ID, operation)
		return nil, apperrors.NewInternal("internal server error", fmt.Errorf("task enqueue function is required"))
	}

	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		s.releaseAccountSlot(task.TaskID, account.ID, operation)
		return nil, err
	}

	if err := taskRepo.Create(ctx, task); err != nil {
		s.releaseAccountSlot(task.TaskID, account.ID, operation)
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to create task", err)
	}

	if err := enqueue(ctx); err != nil {
		s.log().Error("Failed to enqueue task, rolling back",
			zap.String("task_id", task.TaskID),
			zap.Uint("account_id", account.ID),
			zap.String("operation", operation),
			zap.Error(err))
		s.rollbackQueuedTask(task.TaskID, account.ID, err)
		return nil, apperrors.Wrap(apperrors.ErrCodeTaskCreateFailed, "failed to enqueue task", err)
	}

	return &TaskResponse{
		TaskID: task.TaskID,
		Status: model.TaskStatusPending,
	}, nil
}

func (s *taskService) releaseAccountSlot(taskID string, accountID uint, operation string) {
	accountService, err := s.accountServiceOrError()
	if err != nil {
		s.log().Error("Failed to release account slot: account service missing",
			zap.String("task_id", taskID),
			zap.Uint("account_id", accountID),
			zap.String("operation", operation),
			zap.Error(err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), taskRollbackTimeout)
	defer cancel()

	if err := accountService.DecrementJobs(ctx, accountID); err != nil {
		s.log().Error("Failed to release acquired account slot",
			zap.String("task_id", taskID),
			zap.Uint("account_id", accountID),
			zap.String("operation", operation),
			zap.Error(err))
	}
}

func (s *taskService) rollbackQueuedTask(taskID string, accountID uint, reason error) {
	ctx, cancel := context.WithTimeout(context.Background(), taskRollbackTimeout)
	defer cancel()

	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		s.log().Warn("Failed to rollback task status: task repository missing",
			zap.String("task_id", taskID),
			zap.Error(err))
		s.releaseAccountSlot(taskID, accountID, "enqueue rollback")
		return
	}
	if reason == nil {
		reason = errors.New("failed to enqueue task")
	}
	errorMessage := errorText(reason, "failed to enqueue task")
	now := time.Now()
	transitioned, err := taskRepo.UpdateTerminal(ctx, &model.Task{
		TaskID:       taskID,
		Status:       model.TaskStatusFailed,
		ErrorMessage: errorMessage,
		FinishedAt:   &now,
	})
	if err != nil {
		s.log().Warn("Failed to mark task as failed during rollback",
			zap.String("task_id", taskID),
			zap.Error(err))
		return
	}
	if !transitioned {
		s.log().Debug("Task rollback skipped because task is already terminal",
			zap.String("task_id", taskID))
		return
	}

	s.releaseAccountSlot(taskID, accountID, "enqueue rollback")
}

func (s *taskService) CreateImagineTask(ctx context.Context, req *CreateTaskRequest) (*TaskResponse, error) {
	if req == nil {
		return nil, apperrors.NewInvalidInput("request is required")
	}
	prompt, err := requiredTrimmed("prompt", req.Prompt)
	if err != nil {
		return nil, err
	}
	callbackURL, err := optionalHTTPURLTrimmedIfPresent("callback_url", req.CallbackURL)
	if err != nil {
		return nil, err
	}

	if _, err := s.taskRepositoryOrError(); err != nil {
		return nil, err
	}
	accountService, err := s.accountServiceOrError()
	if err != nil {
		return nil, err
	}

	account, err := accountService.AcquireAvailableAccount(ctx)
	if err != nil {
		return nil, preserveAppErrorOrWrap(apperrors.ErrCodeAccountUnavailable, "no available account", err)
	}

	if account == nil {
		return nil, apperrors.NewAccountUnavailable("all accounts are busy or unhealthy")
	}

	taskID := generateTaskID()
	task := &model.Task{
		TaskID:      taskID,
		AccountID:   &account.ID,
		Type:        model.TaskTypeImagine,
		Prompt:      prompt,
		Status:      model.TaskStatusPending,
		CallbackURL: callbackURL,
	}

	return s.createQueuedTask(ctx, task, account, func(enqueueCtx context.Context) error {
		return s.enqueueTask(enqueueCtx, task, account)
	}, "imagine")
}

func (s *taskService) GetTask(ctx context.Context, taskID string) (*model.Task, error) {
	trimmedTaskID, err := requiredTaskID(taskID)
	if err != nil {
		return nil, err
	}
	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		return nil, err
	}
	return taskRepo.GetByTaskID(ctx, trimmedTaskID)
}

func (s *taskService) ListTasks(ctx context.Context, limit, offset int) ([]model.Task, int64, error) {
	if limit <= 0 {
		return nil, 0, apperrors.NewInvalidInput("limit must be greater than 0")
	}
	if limit > constants.MaxListLimit {
		limit = constants.MaxListLimit
	}
	if offset < 0 {
		return nil, 0, apperrors.NewInvalidInput("offset must be greater than or equal to 0")
	}
	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		return nil, 0, err
	}
	return taskRepo.List(ctx, limit, offset)
}

type TaskMessage struct {
	Kind      QueueMessageKind `json:"kind"`
	TaskID    string           `json:"task_id"`
	Prompt    string           `json:"prompt"`
	AccountID uint             `json:"account_id"`
}

func QueueMessageKindFromJSON(data string) QueueMessageKind {
	var envelope struct {
		Kind QueueMessageKind `json:"kind"`
	}
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return ""
	}
	return QueueMessageKind(strings.TrimSpace(string(envelope.Kind)))
}

func DecodeImagineTaskMessage(data string) (TaskMessage, error) {
	var msg TaskMessage
	if err := decodeQueueJSON(data, &msg); err != nil {
		return msg, err
	}
	msg.Kind = trimQueueKind(msg.Kind)
	if err := requireQueueMessageKind(msg.Kind, QueueMessageKindImagine); err != nil {
		return msg, err
	}
	msg.TaskID = trimQueueField(msg.TaskID)
	msg.Prompt = trimQueueField(msg.Prompt)
	return msg, nil
}

func decodeQueueJSON(data string, dst any) error {
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("queue message contains multiple JSON values")
		}
		return err
	}
	return nil
}

func trimQueueKind(kind QueueMessageKind) QueueMessageKind {
	return QueueMessageKind(strings.TrimSpace(string(kind)))
}

func trimQueueField(value string) string {
	return strings.TrimSpace(value)
}

func requireQueueMessageKind(actual, expected QueueMessageKind) error {
	if actual != expected {
		return fmt.Errorf("queue message kind must be %q", expected)
	}
	return nil
}

func (s *taskService) enqueueJSON(ctx context.Context, msg any) error {
	if s == nil || s.redis == nil {
		return apperrors.New(apperrors.ErrCodeRedisError, "redis client is required")
	}
	queueName, err := s.queueName()
	if err != nil {
		return err
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return s.redis.LPush(ctx, queueName, data).Err()
}

func (s *taskService) enqueueTask(ctx context.Context, task *model.Task, account *model.Account) error {
	msg := TaskMessage{
		Kind:      QueueMessageKindImagine,
		TaskID:    task.TaskID,
		Prompt:    task.Prompt,
		AccountID: account.ID,
	}

	return s.enqueueJSON(ctx, msg)
}

func (s *taskService) withRetry(ctx context.Context, taskID string, fn func() error) error {
	var lastErr error
	maxRetries := s.maxRetries()
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		lastErr = fn()
		if lastErr == nil {
			break
		}
		if attempt < maxRetries {
			delay := time.Duration(attempt+1) * 2 * time.Second
			s.log().Warn("Discord API call failed, preparing to retry",
				zap.String("task_id", taskID),
				zap.Int("attempt", attempt+1),
				zap.Int("max_retries", maxRetries),
				zap.Duration("delay", delay),
				zap.String("error", errorText(lastErr, "discord API call failed")))

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastErr
}

func (s *taskService) ProcessTask(ctx context.Context, msg *TaskMessage) error {
	taskID, prompt, accountID, err := validateTaskMessage(msg)
	if err != nil {
		if taskID != "" {
			s.handleTaskProcessingFailure(taskID, accountID, err)
		}
		return err
	}
	discordAPI, err := s.discordAPIOrError()
	if err != nil {
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return err
	}

	if err := s.markTaskProcessing(ctx, taskID, accountID, "imagine"); err != nil {
		return err
	}

	account, err := s.accountForQueuedTask(ctx, taskID, accountID)
	if err != nil {
		return err
	}

	discordReq := &discord.ImagineRequest{
		Prompt:    prompt,
		GuildID:   account.GuildID,
		ChannelID: account.ChannelID,
		UserToken: account.UserToken,
	}

	if lastErr := s.withRetry(ctx, taskID, func() error {
		return discordAPI.Imagine(ctx, discordReq)
	}); lastErr != nil {
		s.handleDiscordCallFailure(taskID, accountID, lastErr)
		return lastErr
	}

	return s.markTaskSubmitted(ctx, taskID, accountID)
}

func validateTaskMessage(msg *TaskMessage) (taskID string, prompt string, accountID uint, err error) {
	if msg == nil {
		err = apperrors.NewInvalidInput("message is required")
		return
	}

	taskID, err = requiredTaskID(msg.TaskID)
	if err != nil {
		return
	}
	if msg.AccountID == 0 {
		err = apperrors.NewInvalidInput("account_id is required")
		return
	}
	accountID = msg.AccountID

	prompt, err = requiredTrimmed("prompt", msg.Prompt)
	return
}

type QueueStatus struct {
	WaitingInQueue  []QueueItem           `json:"waiting_in_queue"`
	ProcessingTasks []ProcessingQueueItem `json:"processing_tasks"`
	QueueLength     int64                 `json:"queue_length"`
	ProcessingCount int                   `json:"processing_count"`
}

type QueueItem struct {
	Kind         QueueMessageKind `json:"kind"`
	TaskID       string           `json:"task_id"`
	AccountID    uint             `json:"-"`
	Prompt       string           `json:"prompt,omitempty"`
	ImageURL     string           `json:"image_url,omitempty"`
	ParentTaskID string           `json:"parent_task_id,omitempty"`
}

type ProcessingQueueItem struct {
	TaskID       string           `json:"task_id"`
	AccountID    *uint            `json:"-"`
	ParentTaskID string           `json:"parent_task_id,omitempty"`
	Type         model.TaskType   `json:"type"`
	Prompt       string           `json:"prompt,omitempty"`
	Status       model.TaskStatus `json:"status"`
	Progress     int              `json:"progress"`
	ImageURL     string           `json:"image_url,omitempty"`
	OSSImageURL  string           `json:"oss_image_url,omitempty"`
	Description  string           `json:"description,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	FinishedAt   *time.Time       `json:"finished_at,omitempty"`
}

func (s *taskService) GetQueueList(ctx context.Context) (*QueueStatus, error) {
	if s == nil || s.redis == nil {
		return nil, apperrors.New(apperrors.ErrCodeRedisError, "redis client is required")
	}
	queueName, err := s.queueName()
	if err != nil {
		return nil, err
	}

	length, err := s.redis.LLen(ctx, queueName).Result()
	if err != nil {
		return nil, err
	}

	start, stop, inspectCount := queueInspectRange(length, constants.QueueInspectLimit)
	waitingTasks := make([]QueueItem, 0, inspectCount)

	if inspectCount > 0 {
		results, err := s.redis.LRange(ctx, queueName, start, stop).Result()
		if err != nil {
			return nil, err
		}

		for _, result := range results {
			item, ok := parseQueueItem(result)
			if !ok {
				continue
			}
			waitingTasks = append(waitingTasks, item)
		}
	}

	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		return nil, err
	}
	discordActiveTasks, err := taskRepo.GetDiscordActiveTasks(ctx, 100)
	if err != nil {
		return nil, err
	}
	processingTasks := processingQueueItems(discordActiveTasks)

	return &QueueStatus{
		WaitingInQueue:  waitingTasks,
		ProcessingTasks: processingTasks,
		QueueLength:     length,
		ProcessingCount: len(processingTasks),
	}, nil
}

func queueInspectRange(length, limit int64) (start, stop int64, count int) {
	if length <= 0 || limit <= 0 {
		return 0, -1, 0
	}
	if length <= limit {
		return 0, length - 1, int(length)
	}
	return length - limit, length - 1, int(limit)
}

func processingQueueItems(tasks []*model.Task) []ProcessingQueueItem {
	items := make([]ProcessingQueueItem, 0, len(tasks))
	for _, task := range tasks {
		if task == nil {
			continue
		}
		items = append(items, processingQueueItemFromTask(task))
	}
	return items
}

func processingQueueItemFromTask(task *model.Task) ProcessingQueueItem {
	var accountID *uint
	if task.AccountID != nil {
		id := *task.AccountID
		accountID = &id
	}

	return ProcessingQueueItem{
		TaskID:       task.TaskID,
		AccountID:    accountID,
		ParentTaskID: task.ParentTaskID,
		Type:         task.Type,
		Prompt:       task.Prompt,
		Status:       task.Status,
		Progress:     task.Progress,
		ImageURL:     task.ImageURL,
		OSSImageURL:  task.OSSImageURL,
		Description:  task.Description,
		CreatedAt:    task.CreatedAt,
		UpdatedAt:    task.UpdatedAt,
		FinishedAt:   task.FinishedAt,
	}
}

func parseQueueItem(data string) (QueueItem, bool) {
	switch QueueMessageKindFromJSON(data) {
	case QueueMessageKindImagine:
		msg, err := DecodeImagineTaskMessage(data)
		if err == nil && msg.TaskID != "" {
			return queueItemFromImagine(msg), true
		}
	case QueueMessageKindDescribe:
		msg, err := DecodeDescribeTaskMessage(data)
		if err == nil && msg.TaskID != "" {
			return queueItemFromDescribe(msg), true
		}
	case QueueMessageKindAction:
		msg, err := DecodeActionTaskMessage(data)
		if err == nil && msg.TaskID != "" {
			return queueItemFromAction(msg), true
		}
	}

	return QueueItem{}, false
}

func queueItemFromImagine(msg TaskMessage) QueueItem {
	return QueueItem{
		Kind:      QueueMessageKindImagine,
		TaskID:    msg.TaskID,
		AccountID: msg.AccountID,
		Prompt:    msg.Prompt,
	}
}

func queueItemFromDescribe(msg TaskDescribeMessage) QueueItem {
	return QueueItem{
		Kind:      QueueMessageKindDescribe,
		TaskID:    msg.TaskID,
		AccountID: msg.AccountID,
		ImageURL:  msg.ImageURL,
	}
}

func queueItemFromAction(msg TaskActionMessage) QueueItem {
	return QueueItem{
		Kind:         QueueMessageKindAction,
		TaskID:       msg.TaskID,
		AccountID:    msg.AccountID,
		ParentTaskID: msg.ParentTaskID,
	}
}

func generateTaskID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

// ===== Describe Task =====

type CreateDescribeTaskRequest struct {
	ImageURL    string
	CallbackURL string
}

type TaskDescribeMessage struct {
	Kind      QueueMessageKind `json:"kind"`
	TaskID    string           `json:"task_id"`
	ImageURL  string           `json:"image_url"`
	AccountID uint             `json:"account_id"`
}

func DecodeDescribeTaskMessage(data string) (TaskDescribeMessage, error) {
	var msg TaskDescribeMessage
	if err := decodeQueueJSON(data, &msg); err != nil {
		return msg, err
	}
	msg.Kind = trimQueueKind(msg.Kind)
	if err := requireQueueMessageKind(msg.Kind, QueueMessageKindDescribe); err != nil {
		return msg, err
	}
	msg.TaskID = trimQueueField(msg.TaskID)
	msg.ImageURL = trimQueueField(msg.ImageURL)
	return msg, nil
}

func (s *taskService) CreateDescribeTask(ctx context.Context, req *CreateDescribeTaskRequest) (*TaskResponse, error) {
	if req == nil {
		return nil, apperrors.NewInvalidInput("request is required")
	}
	imageURL, err := requiredHTTPURL("image_url", req.ImageURL)
	if err != nil {
		return nil, err
	}
	callbackURL, err := optionalHTTPURLTrimmedIfPresent("callback_url", req.CallbackURL)
	if err != nil {
		return nil, err
	}

	if _, err := s.taskRepositoryOrError(); err != nil {
		return nil, err
	}
	accountService, err := s.accountServiceOrError()
	if err != nil {
		return nil, err
	}

	account, err := accountService.AcquireAvailableAccount(ctx)
	if err != nil {
		return nil, preserveAppErrorOrWrap(apperrors.ErrCodeAccountUnavailable, "no available account", err)
	}
	if account == nil {
		return nil, apperrors.NewAccountUnavailable("all accounts are busy or unhealthy")
	}

	taskID := generateTaskID()
	task := &model.Task{
		TaskID:      taskID,
		AccountID:   &account.ID,
		Type:        model.TaskTypeDescribe,
		Prompt:      imageURL,
		Status:      model.TaskStatusPending,
		CallbackURL: callbackURL,
	}

	return s.createQueuedTask(ctx, task, account, func(enqueueCtx context.Context) error {
		return s.enqueueDescribeTask(enqueueCtx, task, account)
	}, "describe")
}

func (s *taskService) enqueueDescribeTask(ctx context.Context, task *model.Task, account *model.Account) error {
	msg := TaskDescribeMessage{
		Kind:      QueueMessageKindDescribe,
		TaskID:    task.TaskID,
		ImageURL:  task.Prompt,
		AccountID: account.ID,
	}
	return s.enqueueJSON(ctx, msg)
}

func (s *taskService) ProcessDescribeTask(ctx context.Context, msg *TaskDescribeMessage) error {
	taskID, imageURL, accountID, err := validateDescribeTaskMessage(msg)
	if err != nil {
		if taskID != "" {
			s.handleTaskProcessingFailure(taskID, accountID, err)
		}
		return err
	}
	discordAPI, err := s.discordAPIOrError()
	if err != nil {
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return err
	}

	if err := s.markTaskProcessing(ctx, taskID, accountID, "describe"); err != nil {
		return err
	}

	account, err := s.accountForQueuedTask(ctx, taskID, accountID)
	if err != nil {
		return err
	}

	describeReq := &discord.DescribeRequest{
		ImageURL:  imageURL,
		GuildID:   account.GuildID,
		ChannelID: account.ChannelID,
		UserToken: account.UserToken,
	}

	if lastErr := s.withRetry(ctx, taskID, func() error {
		return discordAPI.Describe(ctx, describeReq)
	}); lastErr != nil {
		s.handleDiscordCallFailure(taskID, accountID, lastErr)
		return lastErr
	}

	return s.markTaskSubmitted(ctx, taskID, accountID)
}

func validateDescribeTaskMessage(msg *TaskDescribeMessage) (taskID string, imageURL string, accountID uint, err error) {
	if msg == nil {
		err = apperrors.NewInvalidInput("message is required")
		return
	}

	taskID, err = requiredTaskID(msg.TaskID)
	if err != nil {
		return
	}
	if msg.AccountID == 0 {
		err = apperrors.NewInvalidInput("account_id is required")
		return
	}
	accountID = msg.AccountID

	imageURL, err = requiredHTTPURL("image_url", msg.ImageURL)
	return
}

type TaskActionRequest struct {
	TaskID      string `json:"task_id"`     // Original task ID
	ActionType  string `json:"action_type"` // Operation type
	Index       int    `json:"index"`       // Index: 1-4
	CallbackURL string `json:"callback_url"`
}

// PerformTaskAction
func (s *taskService) PerformTaskAction(ctx context.Context, req *TaskActionRequest) (*TaskResponse, error) {
	taskID, actionType, index, callbackURL, err := validateTaskActionRequest(req)
	if err != nil {
		return nil, err
	}

	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		return nil, err
	}
	accountService, err := s.accountServiceOrError()
	if err != nil {
		return nil, err
	}

	parentTask, err := taskRepo.GetByTaskID(ctx, taskID)
	if err != nil {
		return nil, preserveAppErrorOrWrap(apperrors.ErrCodeTaskNotFound, "parent task not found", err)
	}
	if parentTask == nil {
		return nil, apperrors.NewTaskNotFound(taskID)
	}

	if parentTask.Status != model.TaskStatusSuccess {
		return nil, apperrors.New(apperrors.ErrCodeTaskNotCompleted,
			fmt.Sprintf("parent task is not completed yet, current status: %s", parentTask.Status))
	}

	if parentTask.DiscordMessageID == "" {
		return nil, taskActionUnavailableError()
	}

	if parentTask.Buttons == nil || *parentTask.Buttons == "" {
		return nil, taskActionUnavailableError()
	}

	customID, taskType, err := getCustomIDFromButtonsDynamic(*parentTask.Buttons, actionType, index)
	if err != nil {
		return nil, err
	}

	if parentTask.AccountID == nil {
		return nil, taskActionUnavailableError()
	}

	account, err := accountService.AcquireAccount(ctx, *parentTask.AccountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, apperrors.NewAccountUnavailable("account unavailable")
	}

	// 7. Create new task record
	newTaskID := generateTaskID()
	newTask := &model.Task{
		TaskID:       newTaskID,
		AccountID:    parentTask.AccountID,
		ParentTaskID: taskID,
		Type:         taskType,
		Prompt:       parentTask.Prompt,
		Status:       model.TaskStatusPending,
		CallbackURL:  callbackURL,
	}

	actionMsg := &TaskActionMessage{
		Kind:             QueueMessageKindAction,
		TaskID:           newTaskID,
		ParentTaskID:     taskID,
		CustomID:         customID,
		DiscordMessageID: parentTask.DiscordMessageID,
		AccountID:        account.ID,
	}

	return s.createQueuedTask(ctx, newTask, account, func(enqueueCtx context.Context) error {
		return s.enqueueActionTask(enqueueCtx, actionMsg)
	}, "action")
}

func validateTaskActionRequest(req *TaskActionRequest) (taskID string, actionType string, index int, callbackURL string, err error) {
	if req == nil {
		err = apperrors.NewInvalidInput("request is required")
		return
	}

	taskID, err = requiredTaskID(req.TaskID)
	if err != nil {
		return
	}

	actionType, err = requiredTrimmed("action_type", req.ActionType)
	if err != nil {
		return
	}
	if !isSupportedTaskAction(actionType) {
		err = apperrors.NewInvalidInput("unsupported action_type")
		return
	}

	if req.Index < 1 || req.Index > 4 {
		err = apperrors.NewInvalidInput("index must be between 1 and 4")
		return
	}
	index = req.Index

	callbackURL, err = optionalHTTPURLTrimmedIfPresent("callback_url", req.CallbackURL)
	return
}

func taskActionUnavailableError() error {
	return apperrors.NewInvalidInput("task action is not available for this task")
}

func isSupportedTaskAction(actionType string) bool {
	switch actionType {
	case "upscale", "zoom_out_2x", "zoom_out_1_5x", "upscale_subtle", "upscale_creative":
		return true
	default:
		return false
	}
}

type TaskActionMessage struct {
	Kind             QueueMessageKind `json:"kind"`
	TaskID           string           `json:"task_id"`
	ParentTaskID     string           `json:"parent_task_id"`
	CustomID         string           `json:"custom_id"`
	DiscordMessageID string           `json:"discord_message_id"`
	AccountID        uint             `json:"account_id"`
}

func DecodeActionTaskMessage(data string) (TaskActionMessage, error) {
	var msg TaskActionMessage
	if err := decodeQueueJSON(data, &msg); err != nil {
		return msg, err
	}
	msg.Kind = trimQueueKind(msg.Kind)
	if err := requireQueueMessageKind(msg.Kind, QueueMessageKindAction); err != nil {
		return msg, err
	}
	msg.TaskID = trimQueueField(msg.TaskID)
	msg.ParentTaskID = trimQueueField(msg.ParentTaskID)
	msg.CustomID = trimQueueField(msg.CustomID)
	msg.DiscordMessageID = trimQueueField(msg.DiscordMessageID)
	return msg, nil
}

func (s *taskService) enqueueActionTask(ctx context.Context, msg *TaskActionMessage) error {
	return s.enqueueJSON(ctx, msg)
}

func (s *taskService) ProcessActionTask(ctx context.Context, msg *TaskActionMessage) error {
	taskID, customID, discordMessageID, accountID, err := validateActionTaskMessage(msg)
	if err != nil {
		if taskID != "" {
			s.handleTaskProcessingFailure(taskID, accountID, err)
		}
		return err
	}
	discordAPI, err := s.discordAPIOrError()
	if err != nil {
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return err
	}

	if err := s.markTaskProcessing(ctx, taskID, accountID, "action"); err != nil {
		return err
	}

	account, err := s.accountForQueuedTask(ctx, taskID, accountID)
	if err != nil {
		return err
	}

	discordReq := &discord.ButtonActionRequest{
		CustomID:  customID,
		MessageID: discordMessageID,
		GuildID:   account.GuildID,
		ChannelID: account.ChannelID,
		UserToken: account.UserToken,
	}

	if lastErr := s.withRetry(ctx, taskID, func() error {
		return discordAPI.PerformButtonAction(ctx, discordReq)
	}); lastErr != nil {
		s.handleDiscordCallFailure(taskID, accountID, lastErr)
		return lastErr
	}

	return s.markTaskSubmitted(ctx, taskID, accountID)
}

func validateActionTaskMessage(msg *TaskActionMessage) (taskID string, customID string, discordMessageID string, accountID uint, err error) {
	if msg == nil {
		err = apperrors.NewInvalidInput("message is required")
		return
	}

	taskID, err = requiredTaskID(msg.TaskID)
	if err != nil {
		return
	}
	if msg.AccountID == 0 {
		err = apperrors.NewInvalidInput("account_id is required")
		return
	}
	accountID = msg.AccountID

	customID, err = requiredTrimmed("custom_id", msg.CustomID)
	if err != nil {
		return
	}
	discordMessageID, err = requiredDiscordMessageID(msg.DiscordMessageID)
	return
}

func (s *taskService) markTaskProcessing(ctx context.Context, taskID string, accountID uint, operation string) error {
	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		return err
	}
	if err := taskRepo.UpdateStatus(ctx, taskID, model.TaskStatusProcessing); err != nil {
		if isTaskAlreadyTerminal(err) {
			return err
		}
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return err
	}
	return nil
}

func isTaskAlreadyTerminal(err error) bool {
	var appErr *apperrors.AppError
	return errors.As(err, &appErr) && appErr.Code == apperrors.ErrCodeTaskAlreadyTerminal
}

func (s *taskService) markTaskSubmitted(ctx context.Context, taskID string, accountID uint) error {
	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		return err
	}
	if err := taskRepo.UpdateStatus(ctx, taskID, model.TaskStatusSubmitted); err != nil {
		return err
	}

	accountService, err := s.accountServiceOrError()
	if err != nil {
		s.log().Warn("Failed to mark account healthy after task submission: account service missing",
			zap.String("task_id", taskID),
			zap.Uint("account_id", accountID),
			zap.Error(err))
		return nil
	}
	if err := accountService.SetAccountHealthy(ctx, accountID, true, ""); err != nil {
		s.log().Warn("Failed to mark account healthy after task submission",
			zap.String("task_id", taskID),
			zap.Uint("account_id", accountID),
			zap.Error(err))
	}

	return nil
}

func (s *taskService) accountForQueuedTask(ctx context.Context, taskID string, accountID uint) (*model.Account, error) {
	if accountID == 0 {
		err := apperrors.NewInvalidInput("queued task missing account_id")
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return nil, err
	}

	accountService, err := s.accountServiceOrError()
	if err != nil {
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return nil, err
	}

	accountID, cleanupAccountID, err := s.accountIDForQueuedTask(ctx, taskID, accountID)
	if err != nil {
		s.handleTaskProcessingFailure(taskID, cleanupAccountID, err)
		return nil, err
	}

	account, err := accountService.GetAccountByID(ctx, accountID)
	if err != nil {
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return nil, err
	}
	if account == nil {
		err := apperrors.NewAccountNotFound(accountID)
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return nil, err
	}
	if account.IsDisabled {
		err := apperrors.NewAccountUnavailable("account is disabled")
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return nil, err
	}
	if !account.IsHealthy {
		err := apperrors.NewAccountUnavailable("account is unhealthy")
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return nil, err
	}
	if strings.TrimSpace(account.UserToken) == "" {
		err := apperrors.NewAccountUnavailable("account missing user_token")
		s.handleTaskProcessingFailure(taskID, accountID, err)
		return nil, err
	}

	return account, nil
}

func (s *taskService) accountIDForQueuedTask(ctx context.Context, taskID string, queuedAccountID uint) (accountID uint, cleanupAccountID uint, err error) {
	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		return 0, queuedAccountID, err
	}

	task, err := taskRepo.GetByTaskID(ctx, taskID)
	if err != nil {
		return 0, queuedAccountID, err
	}
	if task == nil {
		return 0, queuedAccountID, apperrors.NewTaskNotFound(taskID)
	}
	if task.AccountID == nil {
		return 0, queuedAccountID, apperrors.NewInvalidInput("queued task has no associated account")
	}

	storedAccountID := *task.AccountID
	if storedAccountID != queuedAccountID {
		return 0, storedAccountID, apperrors.NewInvalidInput("queued task account_id does not match task account_id")
	}

	return storedAccountID, storedAccountID, nil
}

func (s *taskService) handleDiscordCallFailure(taskID string, accountID uint, callErr error) {
	s.handleTaskProcessingFailure(taskID, accountID, callErr)
}

func (s *taskService) RejectQueueMessage(taskID string, accountID uint, reason error) error {
	taskID, err := requiredTaskID(taskID)
	if err != nil {
		return err
	}
	if reason == nil {
		reason = apperrors.NewInvalidInput("queue message rejected")
	}
	if _, err := s.taskRepositoryOrError(); err != nil {
		return err
	}
	s.handleTaskProcessingFailure(taskID, accountID, reason)
	return nil
}

func (s *taskService) handleTaskProcessingFailure(taskID string, accountID uint, callErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), taskRollbackTimeout)
	defer cancel()

	releaseAccountID := s.taskAccountID(ctx, taskID)
	if releaseAccountID == 0 {
		releaseAccountID = accountID
	}

	now := time.Now()
	errorMessage := errorText(callErr, "task processing failed")
	task := &model.Task{
		TaskID:       taskID,
		Status:       model.TaskStatusFailed,
		ErrorMessage: errorMessage,
		FinishedAt:   &now,
	}

	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		s.log().Error("Failed to mark task failed after processing failure",
			zap.String("task_id", taskID),
			zap.Error(err))
		return
	}
	transitioned, err := taskRepo.UpdateTerminal(ctx, task)
	if err != nil {
		s.log().Error("Failed to mark task failed after processing failure",
			zap.String("task_id", taskID),
			zap.Error(err))
		return
	}
	if !transitioned {
		s.log().Debug("Task processing failure skipped because task is already terminal",
			zap.String("task_id", taskID))
		return
	}

	if releaseAccountID == 0 {
		return
	}
	accountService, err := s.accountServiceOrError()
	if err != nil {
		s.log().Error("Failed to record task processing failure on account: account service missing",
			zap.String("task_id", taskID),
			zap.Uint("account_id", releaseAccountID),
			zap.Error(err))
		return
	}
	if err := accountService.DecrementJobs(ctx, releaseAccountID); err != nil {
		s.log().Error("Failed to decrement account jobs after task processing failure",
			zap.String("task_id", taskID),
			zap.Uint("account_id", releaseAccountID),
			zap.Error(err))
	}
	if err := accountService.RecordTaskResult(ctx, releaseAccountID, false, errorMessage); err != nil {
		s.log().Error("Failed to record task processing failure on account",
			zap.String("task_id", taskID),
			zap.Uint("account_id", releaseAccountID),
			zap.Error(err))
	}
}

func errorText(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	message := redact.Text(err.Error())
	if message == "" {
		return fallback
	}
	return message
}

func (s *taskService) taskAccountID(ctx context.Context, taskID string) uint {
	taskRepo, repoErr := s.taskRepositoryOrError()
	if repoErr != nil {
		s.log().Warn("Failed to load task account for failure cleanup",
			zap.String("task_id", taskID),
			zap.Error(repoErr))
		return 0
	}
	task, err := taskRepo.GetByTaskID(ctx, taskID)
	if err != nil {
		s.log().Warn("Failed to load task account for failure cleanup",
			zap.String("task_id", taskID),
			zap.Error(err))
		return 0
	}
	if task == nil || task.AccountID == nil {
		return 0
	}
	return *task.AccountID
}

func (s *taskService) SweepTimedOutTasks(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = constants.TimeoutSweepBatchSize
	}

	taskRepo, err := s.taskRepositoryOrError()
	if err != nil {
		return 0, err
	}
	tasks, err := taskRepo.GetStaleActiveTasks(ctx, cutoff, limit)
	if err != nil {
		return 0, err
	}

	now := time.Now()
	timedOut := 0
	for _, task := range tasks {
		if task == nil {
			continue
		}

		task.Status = model.TaskStatusTimeout
		task.ErrorMessage = taskTimedOutMessage
		task.FinishedAt = &now

		transitioned, err := taskRepo.UpdateTerminal(ctx, task)
		if err != nil {
			return timedOut, err
		}
		if !transitioned {
			continue
		}

		timedOut++
		if task.AccountID != nil {
			s.recordTimedOutAccountResult(ctx, task.TaskID, *task.AccountID)
		}
	}

	return timedOut, nil
}

func (s *taskService) recordTimedOutAccountResult(ctx context.Context, taskID string, accountID uint) {
	accountService, err := s.accountServiceOrError()
	if err != nil {
		s.log().Error("Account service missing; cannot record timed out task result",
			zap.String("task_id", taskID),
			zap.Uint("account_id", accountID),
			zap.Error(err))
		return
	}

	if err := accountService.DecrementJobs(ctx, accountID); err != nil {
		s.log().Error("Failed to release account slot for timed out task",
			zap.String("task_id", taskID),
			zap.Uint("account_id", accountID),
			zap.Error(err))
	}
	if err := accountService.RecordTaskResult(ctx, accountID, false, taskTimedOutMessage); err != nil {
		s.log().Error("Failed to record timed out task result on account",
			zap.String("task_id", taskID),
			zap.Uint("account_id", accountID),
			zap.Error(err))
	}
}

func getCustomIDFromButtonsDynamic(buttonsJSON string, actionType string, index int) (string, model.TaskType, error) {
	var buttons []string
	if err := json.Unmarshal([]byte(buttonsJSON), &buttons); err != nil {
		return "", "", taskActionUnavailableError()
	}

	if len(buttons) == 0 {
		return "", "", taskActionUnavailableError()
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

	return "", "", apperrors.NewInvalidInput(fmt.Sprintf("action_type %q is not available for this task", actionType))
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
