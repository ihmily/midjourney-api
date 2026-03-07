package repository

import (
	"context"
	"time"

	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"gorm.io/gorm"
)

type AccountRepository interface {
	Create(ctx context.Context, account *model.Account) error
	GetByID(ctx context.Context, id uint) (*model.Account, error)
	GetAvailable(ctx context.Context) (*model.Account, error)
	IncrementJobs(ctx context.Context, id uint) error
	DecrementJobs(ctx context.Context, id uint) error
	List(ctx context.Context) ([]model.Account, error)
	Update(ctx context.Context, account *model.Account) error
	Delete(ctx context.Context, id uint) error
	UpdateAccountHealth(ctx context.Context, id uint, health model.AccountHealth, lastError string) error
	RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error
	GetByGuildAndChannel(ctx context.Context, guildID, channelID string) (*model.Account, error)
}

type accountRepository struct {
	db *gorm.DB
}

func NewAccountRepository(db *gorm.DB) AccountRepository {
	return &accountRepository{
		db: db,
	}
}

func (r *accountRepository) Create(ctx context.Context, account *model.Account) error {
	if err := r.db.WithContext(ctx).Create(account).Error; err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *accountRepository) GetByID(ctx context.Context, id uint) (*model.Account, error) {
	var account model.Account
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&account).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, apperrors.NewAccountNotFound(id)
		}
		return nil, apperrors.NewDatabaseError(err)
	}
	return &account, nil
}

func (r *accountRepository) GetAvailable(ctx context.Context) (*model.Account, error) {
	var account model.Account
	err := r.db.WithContext(ctx).
		Where("status = ?", model.AccountStatusActive).
		Where("health = ?", model.AccountHealthHealthy).
		Where("current_jobs < concurrent_limit").
		Order("current_jobs ASC").
		First(&account).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, apperrors.NewAccountUnavailable("All accounts are busy or unhealthy")
		}
		return nil, apperrors.NewDatabaseError(err)
	}
	return &account, nil
}

func (r *accountRepository) IncrementJobs(ctx context.Context, id uint) error {
	result := r.db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).
		Where("current_jobs < concurrent_limit").
		Update("current_jobs", gorm.Expr("current_jobs + ?", 1))

	if result.Error != nil {
		return apperrors.NewDatabaseError(result.Error)
	}

	if result.RowsAffected == 0 {
		return apperrors.New(apperrors.ErrCodeAccountLimitReached, "Account has reached its concurrent limit or does not exist")
	}

	return nil
}

func (r *accountRepository) DecrementJobs(ctx context.Context, id uint) error {
	result := r.db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).
		Where("current_jobs > ?", 0).
		Update("current_jobs", gorm.Expr("current_jobs - ?", 1))

	if result.Error != nil {
		return apperrors.NewDatabaseError(result.Error)
	}

	return nil
}

func (r *accountRepository) List(ctx context.Context) ([]model.Account, error) {
	var accounts []model.Account
	if err := r.db.WithContext(ctx).Find(&accounts).Error; err != nil {
		return nil, apperrors.NewDatabaseError(err)
	}
	return accounts, nil
}

func (r *accountRepository) Update(ctx context.Context, account *model.Account) error {
	if err := r.db.WithContext(ctx).Save(account).Error; err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *accountRepository) Delete(ctx context.Context, id uint) error {
	if err := r.db.WithContext(ctx).Delete(&model.Account{}, id).Error; err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *accountRepository) UpdateAccountHealth(ctx context.Context, id uint, health model.AccountHealth, lastError string) error {
	updates := map[string]interface{}{
		"health":     health,
		"last_error": lastError,
		"updated_at": time.Now(),
	}

	if health == model.AccountHealthHealthy {
		updates["error_count"] = 0
		updates["last_error"] = ""
	}

	err := r.db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).
		Updates(updates).Error

	if err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *accountRepository) RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error {
	currentTime := time.Now()
	updates := map[string]interface{}{
		"last_used_at": currentTime,
		"updated_at":   currentTime,
	}

	if success {
		updates["success_count"] = gorm.Expr("success_count + ?", 1)
		updates["health"] = model.AccountHealthHealthy
		updates["error_count"] = 0
		updates["last_error"] = ""
	} else {
		updates["error_count"] = gorm.Expr("error_count + ?", 1)
		updates["last_error"] = lastError

		var account model.Account
		if err := r.db.WithContext(ctx).Where("id = ?", id).First(&account).Error; err == nil {
			if account.ErrorCount+1 >= constants.MaxErrorCount {
				updates["health"] = model.AccountHealthUnhealthy
			}
		}
	}

	err := r.db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).
		Updates(updates).Error

	if err != nil {
		return apperrors.NewDatabaseError(err)
	}
	return nil
}

func (r *accountRepository) GetByGuildAndChannel(ctx context.Context, guildID, channelID string) (*model.Account, error) {
	var account model.Account
	err := r.db.WithContext(ctx).
		Where("guild_id = ? AND channel_id = ?", guildID, channelID).
		First(&account).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, apperrors.NewDatabaseError(err)
	}
	return &account, nil
}
