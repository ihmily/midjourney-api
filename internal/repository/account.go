package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/redact"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const AccountActiveGuildChannelIndexName = "idx_accounts_active_guild_channel"
const maxAccountLastErrorLength = 512
const accountConfigActiveJobsMessage = "account has active tasks; wait for active tasks to finish before changing listener configuration"
const accountDeleteActiveJobsMessage = "account has active tasks; wait for active tasks to finish before deleting the account"

type AccountRepository interface {
	Create(ctx context.Context, account *model.Account) error
	GetByID(ctx context.Context, id uint) (*model.Account, error)
	AcquireAvailable(ctx context.Context) (*model.Account, error)
	AcquireByID(ctx context.Context, id uint) (*model.Account, error)
	DecrementJobs(ctx context.Context, id uint) error
	List(ctx context.Context) ([]model.Account, error)
	UpdateConfig(ctx context.Context, account *model.Account, resetRuntime bool) error
	Delete(ctx context.Context, id uint) error
	SetAccountHealthy(ctx context.Context, id uint, isHealthy bool, lastError string) error
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

func (r *accountRepository) database() (*gorm.DB, error) {
	if r == nil || r.db == nil {
		return nil, apperrors.NewDatabaseError(fmt.Errorf("account repository database is required"))
	}
	return r.db, nil
}

func (r *accountRepository) Create(ctx context.Context, account *model.Account) error {
	if err := validateAccountForWrite(account); err != nil {
		return err
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	if err := db.WithContext(ctx).Create(account).Error; err != nil {
		return accountWriteError(err, account)
	}
	return nil
}

func (r *accountRepository) GetByID(ctx context.Context, id uint) (*model.Account, error) {
	if err := requireAccountID(id); err != nil {
		return nil, err
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}

	var account model.Account
	err = db.WithContext(ctx).Where("id = ?", id).First(&account).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, apperrors.NewAccountNotFound(id)
		}
		return nil, apperrors.NewDatabaseError(err)
	}
	return &account, nil
}

func (r *accountRepository) AcquireAvailable(ctx context.Context) (*model.Account, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}

	var account model.Account
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("is_disabled = ?", false).
			Where("is_healthy = ?", true).
			Where("current_jobs < concurrent_limit").
			Order("current_jobs ASC, id ASC").
			First(&account).Error; err != nil {
			return err
		}

		if err := acquireAccountSlot(tx, &account); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewAccountUnavailable("all accounts are busy or unhealthy")
		}
		var appErr *apperrors.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, apperrors.NewDatabaseError(err)
	}

	return &account, nil
}

func (r *accountRepository) AcquireByID(ctx context.Context, id uint) (*model.Account, error) {
	if err := requireAccountID(id); err != nil {
		return nil, err
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}

	var account model.Account
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", id).
			First(&account).Error; err != nil {
			return err
		}

		if account.IsDisabled {
			return apperrors.NewAccountUnavailable("account is disabled")
		}
		if !account.IsHealthy {
			return apperrors.NewAccountUnavailable("account is unhealthy")
		}
		if account.CurrentJobs >= account.ConcurrentLimit {
			return apperrors.New(apperrors.ErrCodeAccountLimitReached, "account has reached its concurrent limit")
		}

		if err := acquireAccountSlot(tx, &account); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewAccountNotFound(id)
		}
		var appErr *apperrors.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, apperrors.NewDatabaseError(err)
	}

	return &account, nil
}

func (r *accountRepository) DecrementJobs(ctx context.Context, id uint) error {
	if err := requireAccountID(id); err != nil {
		return err
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	result := db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).
		Update("current_jobs", accountJobDecrementValue())

	return accountUpdateResultError(result, id)
}

func (r *accountRepository) List(ctx context.Context) ([]model.Account, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}

	var accounts []model.Account
	if err := db.WithContext(ctx).Find(&accounts).Error; err != nil {
		return nil, apperrors.NewDatabaseError(err)
	}
	return accounts, nil
}

func (r *accountRepository) UpdateConfig(ctx context.Context, account *model.Account, resetRuntime bool) error {
	if err := validateAccountForWrite(account); err != nil {
		return err
	}
	if err := requireAccountID(account.ID); err != nil {
		return err
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	result := accountConfigUpdateQuery(db.WithContext(ctx), account.ID, resetRuntime).
		Updates(accountConfigUpdates(account, resetRuntime))
	if result == nil {
		return apperrors.NewDatabaseError(fmt.Errorf("account update result is required"))
	}
	if result.Error != nil {
		return accountWriteError(result.Error, account)
	}
	if result.RowsAffected == 0 {
		if resetRuntime {
			return r.accountNoRowsError(ctx, account.ID, accountConfigActiveJobsMessage)
		}
		return apperrors.NewAccountNotFound(account.ID)
	}

	latest, err := r.GetByID(ctx, account.ID)
	if err != nil {
		return err
	}
	if latest != nil {
		*account = *latest
	}
	return nil
}

func accountConfigUpdateQuery(db *gorm.DB, accountID uint, resetRuntime bool) *gorm.DB {
	query := db.Model(&model.Account{}).Where("id = ?", accountID)
	if resetRuntime {
		query = query.Where("current_jobs = ?", 0)
	}
	return query
}

func accountConfigUpdates(account *model.Account, resetRuntime bool) map[string]interface{} {
	updates := map[string]interface{}{
		"guild_id":         account.GuildID,
		"channel_id":       account.ChannelID,
		"user_token":       account.UserToken,
		"is_disabled":      account.IsDisabled,
		"concurrent_limit": account.ConcurrentLimit,
		"updated_at":       time.Now(),
	}
	if resetRuntime {
		updates["is_healthy"] = false
		updates["error_count"] = 0
		updates["last_error"] = ""
	}
	return updates
}

func (r *accountRepository) Delete(ctx context.Context, id uint) error {
	if err := requireAccountID(id); err != nil {
		return err
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	result := idleAccountDeleteQuery(db.WithContext(ctx), id).Delete(&model.Account{})
	return r.accountUpdateResultErrorWithActiveGuard(ctx, result, id, accountDeleteActiveJobsMessage)
}

func idleAccountDeleteQuery(db *gorm.DB, id uint) *gorm.DB {
	return db.Where("id = ?", id).Where("current_jobs = ?", 0)
}

func (r *accountRepository) SetAccountHealthy(ctx context.Context, id uint, isHealthy bool, lastError string) error {
	if err := requireAccountID(id); err != nil {
		return err
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	updates := map[string]interface{}{
		"is_healthy": accountHealthUpdateValue(isHealthy),
		"last_error": sanitizeAccountLastError(lastError),
		"updated_at": time.Now(),
	}

	if isHealthy {
		updates["error_count"] = 0
		updates["last_error"] = ""
	}

	result := db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).
		Updates(updates)

	return accountUpdateResultError(result, id)
}

func (r *accountRepository) RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error {
	if err := requireAccountID(id); err != nil {
		return err
	}
	db, err := r.database()
	if err != nil {
		return err
	}

	currentTime := time.Now()
	updates := taskResultUpdates(success, lastError, currentTime)

	result := db.WithContext(ctx).Model(&model.Account{}).
		Where("id = ?", id).
		Updates(updates)

	return accountUpdateResultError(result, id)
}

func accountUpdateResultError(result *gorm.DB, id uint) error {
	if result == nil {
		return apperrors.NewDatabaseError(fmt.Errorf("account update result is required"))
	}
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return apperrors.NewAccountNotFound(id)
		}
		return apperrors.NewDatabaseError(result.Error)
	}
	if result.RowsAffected == 0 {
		return apperrors.NewAccountNotFound(id)
	}
	return nil
}

func (r *accountRepository) accountUpdateResultErrorWithActiveGuard(ctx context.Context, result *gorm.DB, id uint, activeJobsMessage string) error {
	if result == nil {
		return apperrors.NewDatabaseError(fmt.Errorf("account update result is required"))
	}
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return apperrors.NewAccountNotFound(id)
		}
		return apperrors.NewDatabaseError(result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}
	return r.accountNoRowsError(ctx, id, activeJobsMessage)
}

func (r *accountRepository) accountNoRowsError(ctx context.Context, id uint, activeJobsMessage string) error {
	if strings.TrimSpace(activeJobsMessage) == "" {
		return apperrors.NewAccountNotFound(id)
	}
	if _, err := r.GetByID(ctx, id); err != nil {
		return err
	}
	return apperrors.NewInvalidInput(activeJobsMessage)
}

func taskResultUpdates(success bool, lastError string, currentTime time.Time) map[string]interface{} {
	updates := map[string]interface{}{
		"last_used_at": currentTime,
		"updated_at":   currentTime,
	}

	if success {
		updates["success_count"] = gorm.Expr("success_count + ?", 1)
		updates["is_healthy"] = accountHealthUpdateValue(true)
		updates["error_count"] = 0
		updates["last_error"] = ""
		return updates
	}

	updates["error_count"] = gorm.Expr("error_count + ?", 1)
	updates["last_error"] = sanitizeAccountLastError(lastError)
	updates["is_healthy"] = gorm.Expr(
		"CASE WHEN is_disabled THEN FALSE WHEN error_count + ? >= ? THEN FALSE ELSE is_healthy END",
		1,
		constants.MaxErrorCount,
	)

	return updates
}

func accountJobDecrementValue() interface{} {
	return gorm.Expr("CASE WHEN current_jobs > 0 THEN current_jobs - ? ELSE 0 END", 1)
}

func acquireAccountSlot(db *gorm.DB, account *model.Account) error {
	if account == nil || account.ID == 0 {
		return apperrors.NewInvalidInput("account is required")
	}

	result := accountJobIncrementQuery(db, account.ID).Update("current_jobs", accountJobIncrementValue())
	if result == nil {
		return apperrors.NewDatabaseError(fmt.Errorf("account acquire result is required"))
	}
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperrors.NewAccountUnavailable("account is busy or unhealthy")
	}

	account.CurrentJobs++
	return nil
}

func accountJobIncrementQuery(db *gorm.DB, accountID uint) *gorm.DB {
	return db.Model(&model.Account{}).
		Where("id = ?", accountID).
		Where("is_disabled = ?", false).
		Where("is_healthy = ?", true).
		Where("current_jobs < concurrent_limit")
}

func accountJobIncrementValue() interface{} {
	return gorm.Expr("current_jobs + ?", 1)
}

func sanitizeAccountLastError(lastError string) string {
	return redact.TruncateRunes(redact.Text(lastError), maxAccountLastErrorLength)
}

func accountHealthUpdateValue(isHealthy bool) interface{} {
	if !isHealthy {
		return false
	}
	return gorm.Expr("CASE WHEN is_disabled THEN FALSE ELSE TRUE END")
}

func (r *accountRepository) GetByGuildAndChannel(ctx context.Context, guildID, channelID string) (*model.Account, error) {
	guildID, err := requireAccountField("guild_id", guildID)
	if err != nil {
		return nil, err
	}
	channelID, err = requireAccountField("channel_id", channelID)
	if err != nil {
		return nil, err
	}
	db, err := r.database()
	if err != nil {
		return nil, err
	}

	var account model.Account
	err = db.WithContext(ctx).
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

func validateAccountForWrite(account *model.Account) error {
	if account == nil {
		return apperrors.NewInvalidInput("account is nil")
	}

	guildID, err := requireAccountField("guild_id", account.GuildID)
	if err != nil {
		return err
	}
	channelID, err := requireAccountField("channel_id", account.ChannelID)
	if err != nil {
		return err
	}
	userToken, err := requireAccountField("user_token", account.UserToken)
	if err != nil {
		return err
	}

	account.GuildID = guildID
	account.ChannelID = channelID
	account.UserToken = userToken

	if account.ConcurrentLimit <= 0 {
		return apperrors.NewInvalidInput("concurrent_limit must be greater than 0")
	}
	if account.CurrentJobs < 0 {
		return apperrors.NewInvalidInput("current_jobs must be greater than or equal to 0")
	}
	if account.ErrorCount < 0 {
		return apperrors.NewInvalidInput("error_count must be greater than or equal to 0")
	}
	if account.SuccessCount < 0 {
		return apperrors.NewInvalidInput("success_count must be greater than or equal to 0")
	}
	return nil
}

func requireAccountField(field, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", apperrors.NewInvalidInput(field + " is required")
	}
	if maxLength := accountFieldMaxLength(field); maxLength > 0 && utf8.RuneCountInString(trimmed) > maxLength {
		return "", apperrors.NewInvalidInput(field + " must be at most " + strconv.Itoa(maxLength) + " characters")
	}
	return trimmed, nil
}

func accountFieldMaxLength(field string) int {
	switch field {
	case "guild_id":
		return constants.MaxAccountGuildIDLength
	case "channel_id":
		return constants.MaxAccountChannelIDLength
	case "user_token":
		return constants.MaxAccountUserTokenLength
	default:
		return 0
	}
}

func requireAccountID(id uint) error {
	if id == 0 {
		return apperrors.NewInvalidInput("account id is required")
	}
	return nil
}

func accountWriteError(err error, account *model.Account) error {
	if isActiveGuildChannelUniqueViolation(err) {
		if account == nil {
			return apperrors.New(apperrors.ErrCodeAccountAlreadyExists, "Account already exists")
		}
		return apperrors.NewAccountAlreadyExists(account.GuildID, account.ChannelID)
	}
	return apperrors.NewDatabaseError(err)
}

func isActiveGuildChannelUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == AccountActiveGuildChannelIndexName
}
