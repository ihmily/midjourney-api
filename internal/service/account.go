package service

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/repository"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/redact"
	"go.uber.org/zap"
)

type AccountService interface {
	CreateAccount(ctx context.Context, req *CreateAccountRequest) (*model.Account, error)
	GetAccountByID(ctx context.Context, id uint) (*model.Account, error)
	ListAccounts(ctx context.Context) ([]model.Account, error)
	UpdateAccount(ctx context.Context, id uint, req *UpdateAccountRequest) (*model.Account, error)
	DeleteAccount(ctx context.Context, id uint) error
	AcquireAvailableAccount(ctx context.Context) (*model.Account, error)
	AcquireAccount(ctx context.Context, id uint) (*model.Account, error)
	SetAccountHealthy(ctx context.Context, id uint, isHealthy bool, lastError string) error
	DecrementJobs(ctx context.Context, id uint) error
	RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error
}

type accountService struct {
	accountRepo repository.AccountRepository
	logger      *zap.Logger
}

func NewAccountService(accountRepo repository.AccountRepository, logger *zap.Logger) AccountService {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &accountService{
		accountRepo: accountRepo,
		logger:      logger,
	}
}

func (s *accountService) accountRepositoryOrError() (repository.AccountRepository, error) {
	if s == nil || s.accountRepo == nil {
		return nil, serviceDependencyError("account repository")
	}
	return s.accountRepo, nil
}

type CreateAccountRequest struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	UserToken string `json:"user_token"`
}

type UpdateAccountRequest struct {
	GuildID         *string `json:"guild_id"`
	ChannelID       *string `json:"channel_id"`
	UserToken       *string `json:"user_token"`
	IsDisabled      *bool   `json:"is_disabled"`
	ConcurrentLimit *int    `json:"concurrent_limit"`
}

func (s *accountService) CreateAccount(ctx context.Context, req *CreateAccountRequest) (*model.Account, error) {
	if req == nil {
		return nil, apperrors.NewInvalidInput("request is required")
	}

	guildID, err := requiredAccountField("guild_id", req.GuildID)
	if err != nil {
		return nil, err
	}
	channelID, err := requiredAccountField("channel_id", req.ChannelID)
	if err != nil {
		return nil, err
	}
	userToken, err := requiredAccountField("user_token", req.UserToken)
	if err != nil {
		return nil, err
	}

	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return nil, err
	}

	if err := ensureGuildChannelAvailable(ctx, accountRepo, guildID, channelID, 0); err != nil {
		return nil, err
	}

	account := &model.Account{
		GuildID:         guildID,
		ChannelID:       channelID,
		UserToken:       userToken,
		ConcurrentLimit: constants.DefaultConcurrentLimit,
		IsDisabled:      false,
		IsHealthy:       false,
		CurrentJobs:     0,
	}

	err = accountRepo.Create(ctx, account)
	if err != nil {
		return nil, err
	}

	return account, nil
}

func (s *accountService) GetAccountByID(ctx context.Context, id uint) (*model.Account, error) {
	if err := requireAccountID(id); err != nil {
		return nil, err
	}
	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return nil, err
	}
	account, err := accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	sanitizeAccountRuntimeText(account)
	return account, nil
}

func (s *accountService) ListAccounts(ctx context.Context) ([]model.Account, error) {
	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return nil, err
	}
	accounts, err := accountRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		sanitizeAccountRuntimeText(&accounts[i])
	}
	return accounts, nil
}

func (s *accountService) UpdateAccount(ctx context.Context, id uint, req *UpdateAccountRequest) (*model.Account, error) {
	if err := requireAccountID(id); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, apperrors.NewInvalidInput("request is required")
	}
	if !updateAccountRequestHasChanges(req) {
		return nil, apperrors.NewInvalidInput("at least one account field is required")
	}
	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return nil, err
	}

	account, err := accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, apperrors.NewAccountNotFound(id)
	}

	guildID := account.GuildID
	channelID := account.ChannelID
	configChanged := false
	listenerConfigChanged := false
	wasDisabled := account.IsDisabled

	if req.GuildID != nil {
		trimmed, err := optionalAccountField("guild_id", *req.GuildID)
		if err != nil {
			return nil, err
		}
		if trimmed != account.GuildID {
			listenerConfigChanged = true
		}
		guildID = trimmed
		configChanged = configChanged || trimmed != account.GuildID
	}
	if req.ChannelID != nil {
		trimmed, err := optionalAccountField("channel_id", *req.ChannelID)
		if err != nil {
			return nil, err
		}
		if trimmed != account.ChannelID {
			listenerConfigChanged = true
		}
		channelID = trimmed
		configChanged = configChanged || trimmed != account.ChannelID
	}
	if req.UserToken != nil {
		trimmed, err := optionalAccountField("user_token", *req.UserToken)
		if err != nil {
			return nil, err
		}
		if trimmed != account.UserToken {
			listenerConfigChanged = true
		}
		account.UserToken = trimmed
	}

	if req.IsDisabled != nil {
		account.IsDisabled = *req.IsDisabled
	}
	if req.ConcurrentLimit != nil {
		if *req.ConcurrentLimit < 1 {
			return nil, apperrors.NewInvalidInput("concurrent_limit must be greater than 0")
		}
		account.ConcurrentLimit = *req.ConcurrentLimit
	}
	if account.CurrentJobs > 0 && accountUpdateInterruptsActiveJobs(wasDisabled, account.IsDisabled, listenerConfigChanged) {
		return nil, accountActiveJobsError("wait for active tasks to finish before changing listener configuration")
	}
	if configChanged {
		if err := ensureGuildChannelAvailable(ctx, accountRepo, guildID, channelID, account.ID); err != nil {
			return nil, err
		}
		account.GuildID = guildID
		account.ChannelID = channelID
	}
	resetRuntime := shouldResetAccountHealth(wasDisabled, account.IsDisabled, listenerConfigChanged)
	if resetRuntime {
		account.IsHealthy = false
		account.ErrorCount = 0
		account.LastError = ""
	}

	err = accountRepo.UpdateConfig(ctx, account, resetRuntime)
	if err != nil {
		return nil, err
	}

	sanitizeAccountRuntimeText(account)
	return account, nil
}

func shouldResetAccountHealth(wasDisabled, isDisabled, listenerConfigChanged bool) bool {
	return listenerConfigChanged || isDisabled || (wasDisabled && !isDisabled)
}

func accountUpdateInterruptsActiveJobs(wasDisabled, isDisabled, listenerConfigChanged bool) bool {
	return listenerConfigChanged || (!wasDisabled && isDisabled)
}

func updateAccountRequestHasChanges(req *UpdateAccountRequest) bool {
	if req == nil {
		return false
	}
	return req.GuildID != nil ||
		req.ChannelID != nil ||
		req.UserToken != nil ||
		req.IsDisabled != nil ||
		req.ConcurrentLimit != nil
}

func accountActiveJobsError(action string) error {
	return apperrors.NewInvalidInput("account has active tasks; " + action)
}

func (s *accountService) DeleteAccount(ctx context.Context, id uint) error {
	if err := requireAccountID(id); err != nil {
		return err
	}

	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return err
	}
	account, err := accountRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if account == nil {
		return apperrors.NewAccountNotFound(id)
	}
	if account.CurrentJobs > 0 {
		return accountActiveJobsError("wait for active tasks to finish before deleting the account")
	}
	return accountRepo.Delete(ctx, id)
}

func (s *accountService) AcquireAvailableAccount(ctx context.Context) (*model.Account, error) {
	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return nil, err
	}
	return accountRepo.AcquireAvailable(ctx)
}

func (s *accountService) AcquireAccount(ctx context.Context, id uint) (*model.Account, error) {
	if err := requireAccountID(id); err != nil {
		return nil, err
	}
	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return nil, err
	}
	return accountRepo.AcquireByID(ctx, id)
}

func (s *accountService) SetAccountHealthy(ctx context.Context, id uint, isHealthy bool, lastError string) error {
	if err := requireAccountID(id); err != nil {
		return err
	}
	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return err
	}
	return accountRepo.SetAccountHealthy(ctx, id, isHealthy, lastError)
}

func (s *accountService) DecrementJobs(ctx context.Context, id uint) error {
	if err := requireAccountID(id); err != nil {
		return err
	}
	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return err
	}
	return accountRepo.DecrementJobs(ctx, id)
}

func (s *accountService) RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error {
	if err := requireAccountID(id); err != nil {
		return err
	}
	accountRepo, err := s.accountRepositoryOrError()
	if err != nil {
		return err
	}
	return accountRepo.RecordTaskResult(ctx, id, success, lastError)
}

func requireAccountID(id uint) error {
	if id == 0 {
		return apperrors.NewInvalidInput("account id is required")
	}
	return nil
}

func sanitizeAccountRuntimeText(account *model.Account) {
	if account == nil {
		return
	}
	account.LastError = redact.Text(account.LastError)
}

func requiredTrimmed(field, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", apperrors.NewInvalidInput(field + " is required")
	}
	return trimmed, nil
}

func optionalTrimmed(field, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", apperrors.NewInvalidInput(field + " cannot be blank")
	}
	return trimmed, nil
}

func requiredAccountField(field, value string) (string, error) {
	trimmed, err := requiredTrimmed(field, value)
	if err != nil {
		return "", err
	}
	if err := validateAccountFieldLength(field, trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

func optionalAccountField(field, value string) (string, error) {
	trimmed, err := optionalTrimmed(field, value)
	if err != nil {
		return "", err
	}
	if err := validateAccountFieldLength(field, trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

func validateAccountFieldLength(field, value string) error {
	maxLength := accountFieldMaxLength(field)
	if maxLength == 0 {
		return nil
	}
	if utf8.RuneCountInString(value) > maxLength {
		return apperrors.NewInvalidInput(field + " must be at most " + strconv.Itoa(maxLength) + " characters")
	}
	return nil
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

func ensureGuildChannelAvailable(ctx context.Context, accountRepo repository.AccountRepository, guildID, channelID string, currentAccountID uint) error {
	existing, err := accountRepo.GetByGuildAndChannel(ctx, guildID, channelID)
	if err != nil {
		return err
	}
	if existing != nil && existing.ID != currentAccountID {
		return apperrors.NewAccountAlreadyExists(guildID, channelID)
	}
	return nil
}
