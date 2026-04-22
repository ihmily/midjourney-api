package service

import (
	"context"

	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/repository"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"go.uber.org/zap"
)

type AccountService interface {
	CreateAccount(ctx context.Context, req *CreateAccountRequest) (*model.Account, error)
	GetAccountByID(ctx context.Context, id uint) (*model.Account, error)
	ListAccounts(ctx context.Context) ([]model.Account, error)
	UpdateAccount(ctx context.Context, id uint, req *UpdateAccountRequest) (*model.Account, error)
	DeleteAccount(ctx context.Context, id uint) error
	GetAvailableAccount(ctx context.Context) (*model.Account, error)
	CheckAccountHealth(account *model.Account) (bool, string)
	UpdateAccountHealth(ctx context.Context, id uint, health model.AccountHealth, lastError string) error
	IncrementJobs(ctx context.Context, id uint) error
	DecrementJobs(ctx context.Context, id uint) error
	RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error
}

type accountService struct {
	accountRepo repository.AccountRepository
	logger      *zap.Logger
}

func NewAccountService(accountRepo repository.AccountRepository, logger *zap.Logger) AccountService {
	return &accountService{
		accountRepo: accountRepo,
		logger:      logger,
	}
}

type CreateAccountRequest struct {
	GuildID         string `json:"guild_id" binding:"required"`
	ChannelID       string `json:"channel_id" binding:"required"`
	UserToken       string `json:"user_token" binding:"required"`
	ConcurrentLimit int    `json:"concurrent_limit"`
}

type UpdateAccountRequest struct {
	GuildID         string              `json:"guild_id"`
	ChannelID       string              `json:"channel_id"`
	UserToken       string              `json:"user_token"`
	Status          model.AccountStatus `json:"status"`
	Health          model.AccountHealth `json:"health"`
	ConcurrentLimit int                 `json:"concurrent_limit" binding:"min=1,max=10"`
}

func (s *accountService) CreateAccount(ctx context.Context, req *CreateAccountRequest) (*model.Account, error) {
	account := &model.Account{
		GuildID:   req.GuildID,
		ChannelID: req.ChannelID,
		UserToken: req.UserToken,
		ConcurrentLimit: func() int {
			if req.ConcurrentLimit <= 0 {
				return constants.DefaultConcurrentLimit
			}
			return req.ConcurrentLimit
		}(),
		Status:      model.AccountStatusActive,
		CurrentJobs: 0,
	}

	err := s.accountRepo.Create(ctx, account)
	if err != nil {
		return nil, err
	}

	return account, nil
}

func (s *accountService) GetAccountByID(ctx context.Context, id uint) (*model.Account, error) {
	return s.accountRepo.GetByID(ctx, id)
}

func (s *accountService) ListAccounts(ctx context.Context) ([]model.Account, error) {
	return s.accountRepo.List(ctx)
}

func (s *accountService) UpdateAccount(ctx context.Context, id uint, req *UpdateAccountRequest) (*model.Account, error) {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, apperrors.NewAccountNotFound(id)
	}

	if req.GuildID != "" {
		account.GuildID = req.GuildID
	}
	if req.ChannelID != "" {
		account.ChannelID = req.ChannelID
	}
	if req.UserToken != "" {
		account.UserToken = req.UserToken
	}
	if req.Status != "" {
		account.Status = req.Status
	}
	if req.ConcurrentLimit != 0 {
		account.ConcurrentLimit = req.ConcurrentLimit
	}
	if req.Health != "" {
		account.Health = req.Health
	}

	err = s.accountRepo.Update(ctx, account)
	if err != nil {
		return nil, err
	}

	return account, nil
}

func (s *accountService) DeleteAccount(ctx context.Context, id uint) error {
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if account == nil {
		return apperrors.NewAccountNotFound(id)
	}
	return s.accountRepo.Delete(ctx, id)
}

func (s *accountService) GetAvailableAccount(ctx context.Context) (*model.Account, error) {
	return s.accountRepo.GetAvailable(ctx)
}

func (s *accountService) CheckAccountHealth(account *model.Account) (bool, string) {
	if account.Status != model.AccountStatusActive {
		return false, "account is not active"
	}

	if account.CurrentJobs >= account.ConcurrentLimit {
		return false, "concurrent limit reached"
	}

	if account.Health != model.AccountHealthHealthy {
		return false, "account health is not healthy"
	}

	if account.ErrorCount >= constants.MaxErrorCount {
		return false, "too many recent errors"
	}

	return true, ""
}

func (s *accountService) UpdateAccountHealth(ctx context.Context, id uint, health model.AccountHealth, lastError string) error {
	return s.accountRepo.UpdateAccountHealth(ctx, id, health, lastError)
}

func (s *accountService) IncrementJobs(ctx context.Context, id uint) error {
	return s.accountRepo.IncrementJobs(ctx, id)
}

func (s *accountService) DecrementJobs(ctx context.Context, id uint) error {
	return s.accountRepo.DecrementJobs(ctx, id)
}

func (s *accountService) RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error {
	return s.accountRepo.RecordTaskResult(ctx, id, success, lastError)
}
