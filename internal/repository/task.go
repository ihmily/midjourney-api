package repository

import (
	"context"

	"github.com/trae/midjourney-api/internal/model"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"gorm.io/gorm"
)

type TaskRepository interface {
	Create(ctx context.Context, task *model.Task) error
	GetByTaskID(ctx context.Context, taskID string) (*model.Task, error)
	Update(ctx context.Context, task *model.Task) error
	UpdateStatus(ctx context.Context, taskID string, status model.TaskStatus) error
	UpdateOSSImageURL(ctx context.Context, taskID string, ossURL string) error
	List(ctx context.Context, userID uint, limit, offset int) ([]model.Task, int64, error)
	GetByDiscordMessageID(ctx context.Context, messageID string) (*model.Task, error)
	GetPendingTasks(ctx context.Context, limit int) ([]*model.Task, error)
}

type taskRepository struct {
	db *gorm.DB
}

func NewTaskRepository(db *gorm.DB) TaskRepository {
	return &taskRepository{
		db: db,
	}
}

func (r *taskRepository) Create(ctx context.Context, task *model.Task) error {
	if err := r.db.WithContext(ctx).Create(task).Error; err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *taskRepository) GetByTaskID(ctx context.Context, taskID string) (*model.Task, error) {
	var task model.Task
	err := r.db.WithContext(ctx).Where("task_id = ?", taskID).First(&task).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, apperrors.NewTaskNotFound(taskID)
		}
		return nil, apperrors.NewDatabaseError(err)
	}
	return &task, nil
}

func (r *taskRepository) Update(ctx context.Context, task *model.Task) error {
	if err := r.db.WithContext(ctx).Save(task).Error; err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *taskRepository) UpdateStatus(ctx context.Context, taskID string, status model.TaskStatus) error {
	err := r.db.WithContext(ctx).Model(&model.Task{}).
		Where("task_id = ?", taskID).
		Update("status", status).Error
	if err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *taskRepository) UpdateOSSImageURL(ctx context.Context, taskID string, ossURL string) error {
	err := r.db.WithContext(ctx).Model(&model.Task{}).
		Where("task_id = ?", taskID).
		Update("oss_image_url", ossURL).Error
	if err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *taskRepository) List(ctx context.Context, userID uint, limit, offset int) ([]model.Task, int64, error) {
	var tasks []model.Task
	var total int64

	query := r.db.WithContext(ctx).Model(&model.Task{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, apperrors.NewDatabaseError(err)
	}

	err := query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&tasks).Error
	if err != nil {
		return nil, 0, apperrors.NewDatabaseError(err)
	}
	return tasks, total, nil
}

func (r *taskRepository) GetByDiscordMessageID(ctx context.Context, messageID string) (*model.Task, error) {
	var task model.Task
	err := r.db.WithContext(ctx).Where("discord_message_id = ?", messageID).First(&task).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, apperrors.NewDatabaseError(err)
	}
	return &task, nil
}

func (r *taskRepository) GetPendingTasks(ctx context.Context, limit int) ([]*model.Task, error) {
	var tasks []*model.Task
	err := r.db.WithContext(ctx).Where("status IN ?", []model.TaskStatus{
		model.TaskStatusSubmitted,
		model.TaskStatusInQueue,
		model.TaskStatusProcessing,
	}).Order("created_at DESC").Limit(limit).Find(&tasks).Error
	if err != nil {
		return nil, apperrors.NewDatabaseError(err)
	}
	return tasks, nil
}
