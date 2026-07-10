package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/redact"
	"gorm.io/gorm"
)

const maxTaskErrorMessageLength = 2048

type TaskRepository interface {
	Create(ctx context.Context, task *model.Task) error
	GetByTaskID(ctx context.Context, taskID string) (*model.Task, error)
	Update(ctx context.Context, task *model.Task) error
	UpdateTerminal(ctx context.Context, task *model.Task) (bool, error)
	UpdateStatus(ctx context.Context, taskID string, status model.TaskStatus) error
	UpdateOSSImageURL(ctx context.Context, taskID string, ossURL string) error
	List(ctx context.Context, limit, offset int) ([]model.Task, int64, error)
	GetByDiscordMessageID(ctx context.Context, messageID string) (*model.Task, error)
	GetDiscordActiveTasks(ctx context.Context, limit int) ([]*model.Task, error)
	GetStaleActiveTasks(ctx context.Context, cutoff time.Time, limit int) ([]*model.Task, error)
}

type taskRepository struct {
	db *gorm.DB
}

func NewTaskRepository(db *gorm.DB) TaskRepository {
	return &taskRepository{
		db: db,
	}
}

func (r *taskRepository) database() (*gorm.DB, error) {
	if r == nil || r.db == nil {
		return nil, apperrors.NewDatabaseError(fmt.Errorf("task repository database is required"))
	}
	return r.db, nil
}

func (r *taskRepository) Create(ctx context.Context, task *model.Task) error {
	if err := validateTaskForCreate(task); err != nil {
		return err
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	if err := db.WithContext(ctx).Create(task).Error; err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *taskRepository) GetByTaskID(ctx context.Context, taskID string) (*model.Task, error) {
	taskID, err := requireTaskID(taskID)
	if err != nil {
		return nil, err
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}

	var task model.Task
	err = db.WithContext(ctx).Where("task_id = ?", taskID).First(&task).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, apperrors.NewTaskNotFound(taskID)
		}
		return nil, apperrors.NewDatabaseError(err)
	}
	return &task, nil
}

func (r *taskRepository) Update(ctx context.Context, task *model.Task) error {
	if err := validateTaskForStateUpdate(task); err != nil {
		return err
	}
	if model.IsTerminalTaskStatus(task.Status) {
		return apperrors.NewInvalidInput("use UpdateTerminal for terminal task status")
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	result := db.WithContext(ctx).Model(&model.Task{}).
		Where("task_id = ?", task.TaskID).
		Where("status NOT IN ?", model.TerminalTaskStatuses()).
		Updates(taskStateUpdates(task))

	return r.taskStatusUpdateResultError(ctx, result, task.TaskID)
}

func taskStateUpdates(task *model.Task) map[string]interface{} {
	return map[string]interface{}{
		"discord_message_id": task.DiscordMessageID,
		"status":             task.Status,
		"progress":           task.Progress,
		"buttons":            task.Buttons,
		"updated_at":         time.Now(),
	}
}

func (r *taskRepository) UpdateTerminal(ctx context.Context, task *model.Task) (bool, error) {
	if task == nil {
		return false, apperrors.NewInvalidInput("task is nil")
	}
	taskID, err := requireTaskID(task.TaskID)
	if err != nil {
		return false, err
	}
	task.TaskID = taskID
	if err := requireTerminalTaskStatus(task.Status); err != nil {
		return false, err
	}
	discordMessageID, err := optionalDiscordMessageID(task.DiscordMessageID)
	if err != nil {
		return false, err
	}
	task.DiscordMessageID = discordMessageID
	if err := requireTaskProgress(task.Progress); err != nil {
		return false, err
	}
	ensureTaskFinishedAt(task)
	db, err := r.database()
	if err != nil {
		return false, err
	}

	result := db.WithContext(ctx).Model(&model.Task{}).
		Where("task_id = ?", taskID).
		Where("status NOT IN ?", model.TerminalTaskStatuses()).
		Updates(terminalTaskUpdates(task))

	return r.terminalTaskUpdateResult(ctx, result, taskID)
}

func (r *taskRepository) UpdateStatus(ctx context.Context, taskID string, status model.TaskStatus) error {
	taskID, err := requireTaskID(taskID)
	if err != nil {
		return err
	}
	if err := requireKnownTaskStatus(status); err != nil {
		return err
	}
	if model.IsTerminalTaskStatus(status) {
		return apperrors.NewInvalidInput("use UpdateTerminal for terminal task status")
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	result := db.WithContext(ctx).Model(&model.Task{}).
		Where("task_id = ?", taskID).
		Where("status NOT IN ?", model.TerminalTaskStatuses()).
		Updates(taskStatusUpdates(status))

	return r.taskStatusUpdateResultError(ctx, result, taskID)
}

func taskStatusUpdates(status model.TaskStatus) map[string]interface{} {
	return map[string]interface{}{
		"status":     status,
		"updated_at": time.Now(),
	}
}

func (r *taskRepository) UpdateOSSImageURL(ctx context.Context, taskID string, ossURL string) error {
	taskID, err := requireTaskID(taskID)
	if err != nil {
		return err
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	result := taskOSSImageURLUpdateQuery(db.WithContext(ctx), taskID).
		Updates(taskOSSImageURLUpdates(ossURL))

	return r.ossImageURLUpdateResultError(ctx, result, taskID)
}

func taskOSSImageURLUpdateQuery(db *gorm.DB, taskID string) *gorm.DB {
	return db.Model(&model.Task{}).
		Where("task_id = ?", taskID).
		Where("status = ?", model.TaskStatusSuccess)
}

func taskOSSImageURLUpdates(ossURL string) map[string]interface{} {
	return map[string]interface{}{
		"oss_image_url": ossURL,
		"updated_at":    time.Now(),
	}
}

func (r *taskRepository) List(ctx context.Context, limit, offset int) ([]model.Task, int64, error) {
	if limit <= 0 {
		return nil, 0, apperrors.NewInvalidInput("limit must be greater than 0")
	}
	if offset < 0 {
		return nil, 0, apperrors.NewInvalidInput("offset must be greater than or equal to 0")
	}
	db, err := r.database()
	if err != nil {
		return nil, 0, err
	}

	var tasks []model.Task
	var total int64

	query := db.WithContext(ctx).Model(&model.Task{})

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, apperrors.NewDatabaseError(err)
	}

	err = query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&tasks).Error
	if err != nil {
		return nil, 0, apperrors.NewDatabaseError(err)
	}
	return tasks, total, nil
}

func (r *taskRepository) GetByDiscordMessageID(ctx context.Context, messageID string) (*model.Task, error) {
	messageID, err := requireDiscordMessageID(messageID)
	if err != nil {
		return nil, err
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}

	var task model.Task
	err = db.WithContext(ctx).Where("discord_message_id = ?", messageID).First(&task).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, apperrors.NewDatabaseError(err)
	}
	return &task, nil
}

func (r *taskRepository) GetDiscordActiveTasks(ctx context.Context, limit int) ([]*model.Task, error) {
	if limit <= 0 {
		return nil, apperrors.NewInvalidInput("limit must be greater than 0")
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}

	var tasks []*model.Task
	err = db.WithContext(ctx).
		Where("status IN ?", model.DiscordActiveTaskStatuses()).
		Order("created_at DESC").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil, apperrors.NewDatabaseError(err)
	}
	return tasks, nil
}

func (r *taskRepository) GetStaleActiveTasks(ctx context.Context, cutoff time.Time, limit int) ([]*model.Task, error) {
	if limit <= 0 {
		return nil, apperrors.NewInvalidInput("limit must be greater than 0")
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}

	var tasks []*model.Task
	err = db.WithContext(ctx).
		Where("status IN ?", model.ActiveTaskStatuses()).
		Where("updated_at < ?", cutoff).
		Order("updated_at ASC").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil, apperrors.NewDatabaseError(err)
	}
	return tasks, nil
}

func validateTaskForCreate(task *model.Task) error {
	if task == nil {
		return apperrors.NewInvalidInput("task is nil")
	}

	taskID, err := requireTaskID(task.TaskID)
	if err != nil {
		return err
	}
	task.TaskID = taskID
	parentTaskID, err := optionalTaskIDField("parent_task_id", task.ParentTaskID)
	if err != nil {
		return err
	}
	task.ParentTaskID = parentTaskID
	discordMessageID, err := optionalDiscordMessageID(task.DiscordMessageID)
	if err != nil {
		return err
	}
	task.DiscordMessageID = discordMessageID

	if err := requireKnownTaskStatus(task.Status); err != nil {
		return err
	}
	if err := requireKnownTaskType(task.Type); err != nil {
		return err
	}
	if err := requireTaskProgress(task.Progress); err != nil {
		return err
	}
	return nil
}

func validateTaskForStateUpdate(task *model.Task) error {
	if task == nil {
		return apperrors.NewInvalidInput("task is nil")
	}

	taskID, err := requireTaskID(task.TaskID)
	if err != nil {
		return err
	}
	task.TaskID = taskID
	discordMessageID, err := optionalDiscordMessageID(task.DiscordMessageID)
	if err != nil {
		return err
	}
	task.DiscordMessageID = discordMessageID

	if err := requireKnownTaskStatus(task.Status); err != nil {
		return err
	}
	if err := requireTaskProgress(task.Progress); err != nil {
		return err
	}
	return nil
}

func ensureTaskFinishedAt(task *model.Task) {
	if task == nil || task.FinishedAt != nil {
		return
	}
	now := time.Now()
	task.FinishedAt = &now
}

func requireKnownTaskStatus(status model.TaskStatus) error {
	if status == "" {
		return apperrors.NewInvalidInput("status is required")
	}
	if !model.IsKnownTaskStatus(status) {
		return apperrors.NewInvalidInput("invalid task status: " + string(status))
	}
	return nil
}

func requireTerminalTaskStatus(status model.TaskStatus) error {
	if err := requireKnownTaskStatus(status); err != nil {
		return err
	}
	if !model.IsTerminalTaskStatus(status) {
		return apperrors.NewInvalidInput("terminal task status is required")
	}
	return nil
}

func requireKnownTaskType(taskType model.TaskType) error {
	if taskType == "" {
		return apperrors.NewInvalidInput("type is required")
	}
	if !model.IsKnownTaskType(taskType) {
		return apperrors.NewInvalidInput("invalid task type: " + string(taskType))
	}
	return nil
}

func requireTaskProgress(progress int) error {
	if !model.IsValidTaskProgress(progress) {
		return apperrors.NewInvalidInput("progress must be between " +
			strconv.Itoa(constants.MinTaskProgress) + " and " +
			strconv.Itoa(constants.MaxTaskProgress))
	}
	return nil
}

func requireTaskID(taskID string) (string, error) {
	return requireLimitedTaskField("task_id", taskID, constants.MaxTaskIDLength)
}

func requireDiscordMessageID(messageID string) (string, error) {
	return requireLimitedTaskField("discord_message_id", messageID, constants.MaxDiscordMessageIDLength)
}

func optionalTaskIDField(field, value string) (string, error) {
	return optionalLimitedTaskField(field, value, constants.MaxTaskIDLength)
}

func optionalDiscordMessageID(messageID string) (string, error) {
	return optionalLimitedTaskField("discord_message_id", messageID, constants.MaxDiscordMessageIDLength)
}

func requireLimitedTaskField(field, value string, maxLength int) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", apperrors.NewInvalidInput(field + " is required")
	}
	if err := validateTaskFieldLength(field, trimmed, maxLength); err != nil {
		return "", err
	}
	return trimmed, nil
}

func optionalLimitedTaskField(field, value string, maxLength int) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if err := validateTaskFieldLength(field, trimmed, maxLength); err != nil {
		return "", err
	}
	return trimmed, nil
}

func validateTaskFieldLength(field, value string, maxLength int) error {
	if maxLength <= 0 {
		return nil
	}
	if utf8.RuneCountInString(value) > maxLength {
		return apperrors.NewInvalidInput(field + " must be at most " + strconv.Itoa(maxLength) + " characters")
	}
	return nil
}

func terminalTaskUpdates(task *model.Task) map[string]interface{} {
	updates := map[string]interface{}{
		"status":        task.Status,
		"progress":      task.Progress,
		"error_message": sanitizeTaskErrorMessage(task.ErrorMessage),
		"finished_at":   task.FinishedAt,
		"updated_at":    time.Now(),
	}

	if task.DiscordMessageID != "" {
		updates["discord_message_id"] = task.DiscordMessageID
	}
	if task.ImageURL != "" {
		updates["image_url"] = task.ImageURL
	}
	if task.Buttons != nil {
		updates["buttons"] = task.Buttons
	}
	if task.Description != "" {
		updates["description"] = task.Description
	}

	return updates
}

func sanitizeTaskErrorMessage(message string) string {
	return redact.TruncateRunes(redact.Text(message), maxTaskErrorMessageLength)
}

func (r *taskRepository) ossImageURLUpdateResultError(ctx context.Context, result *gorm.DB, taskID string) error {
	if result == nil {
		return apperrors.NewDatabaseError(fmt.Errorf("task oss image update result is required"))
	}
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return apperrors.NewTaskNotFound(taskID)
		}
		return apperrors.NewDatabaseError(result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}

	db, err := r.database()
	if err != nil {
		return err
	}

	var task model.Task
	if err := db.WithContext(ctx).Select("status").Where("task_id = ?", taskID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperrors.NewTaskNotFound(taskID)
		}
		return apperrors.NewDatabaseError(err)
	}
	if task.Status != model.TaskStatusSuccess {
		return apperrors.NewInvalidInput("oss_image_url can only be updated for successful tasks")
	}
	return nil
}

func (r *taskRepository) taskStatusUpdateResultError(ctx context.Context, result *gorm.DB, taskID string) error {
	if result == nil {
		return apperrors.NewDatabaseError(fmt.Errorf("task status update result is required"))
	}
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return apperrors.NewTaskNotFound(taskID)
		}
		return apperrors.NewDatabaseError(result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}

	db, err := r.database()
	if err != nil {
		return err
	}

	var task model.Task
	if err := db.WithContext(ctx).Select("status").Where("task_id = ?", taskID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperrors.NewTaskNotFound(taskID)
		}
		return apperrors.NewDatabaseError(err)
	}

	if model.IsTerminalTaskStatus(task.Status) {
		return apperrors.NewTaskAlreadyTerminal(taskID, string(task.Status))
	}

	return nil
}

func (r *taskRepository) terminalTaskUpdateResult(ctx context.Context, result *gorm.DB, taskID string) (bool, error) {
	if result == nil {
		return false, apperrors.NewDatabaseError(fmt.Errorf("terminal task update result is required"))
	}
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return false, apperrors.NewTaskNotFound(taskID)
		}
		return false, apperrors.NewDatabaseError(result.Error)
	}
	if result.RowsAffected > 0 {
		return true, nil
	}

	db, err := r.database()
	if err != nil {
		return false, err
	}

	var task model.Task
	if err := db.WithContext(ctx).Select("status").Where("task_id = ?", taskID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, apperrors.NewTaskNotFound(taskID)
		}
		return false, apperrors.NewDatabaseError(err)
	}

	return false, nil
}
