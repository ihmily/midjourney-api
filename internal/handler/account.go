package handler

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/service"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/redact"
	"github.com/trae/midjourney-api/pkg/response"
)

// ListenerManager manages Discord listener lifecycle for accounts.
type ListenerManager interface {
	StartAccountListener(account *model.Account) error
	StopAccountListener(accountID uint) error
}

type AccountHandler struct {
	accountService  service.AccountService
	listenerManager ListenerManager
}

type AccountView struct {
	ID              uint       `json:"id"`
	GuildID         string     `json:"guild_id"`
	ChannelID       string     `json:"channel_id"`
	IsDisabled      bool       `json:"is_disabled"`
	IsHealthy       bool       `json:"is_healthy"`
	ConcurrentLimit int        `json:"concurrent_limit"`
	CurrentJobs     int        `json:"current_jobs"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	ErrorCount      int        `json:"error_count"`
	SuccessCount    int        `json:"success_count"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type CreateAccountReq struct {
	GuildID   string `json:"guild_id" binding:"required"`
	ChannelID string `json:"channel_id" binding:"required"`
	UserToken string `json:"user_token" binding:"required"`
}

type UpdateAccountReq struct {
	GuildID         *string `json:"guild_id"`
	ChannelID       *string `json:"channel_id"`
	UserToken       *string `json:"user_token"`
	IsDisabled      *bool   `json:"is_disabled"`
	ConcurrentLimit *int    `json:"concurrent_limit"`
}

func NewAccountHandler(accountService service.AccountService, listenerManager ListenerManager) *AccountHandler {
	return &AccountHandler{
		accountService:  accountService,
		listenerManager: listenerManager,
	}
}

// CreateAccount creates a new account with the default concurrent limit and starts its Discord listener
// @Summary Create account
// @Description Create a new Discord account with the default concurrent limit and start its listener. Only guild_id, channel_id, and user_token are accepted.
// @Tags Account
// @Accept json
// @Produce json
// @Param request body CreateAccountReq true "Account info"
// @Success 200 {object} response.Response{data=AccountView} "Success"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/accounts [post]
func (h *AccountHandler) CreateAccount(c *gin.Context) {
	var req CreateAccountReq
	if err := bindStrictJSON(c, &req); err != nil {
		response.Error(c, err)
		return
	}
	accountService, err := h.accountServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}

	ctx := c.Request.Context()
	account, err := accountService.CreateAccount(ctx, &service.CreateAccountRequest{
		GuildID:   req.GuildID,
		ChannelID: req.ChannelID,
		UserToken: req.UserToken,
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	if account == nil {
		response.Error(c, handlerInternalError("created account result is required"))
		return
	}

	h.startAccountListenerAsync(account)

	response.Success(c, accountViewFromModel(account))
}

// DeleteAccount deletes an account and stops its Discord listener
// @Summary Delete account
// @Description Delete the specified account and stop its listener. Accounts with active tasks cannot be deleted until current_jobs is 0.
// @Tags Account
// @Param id path int true "Account ID"
// @Produce json
// @Success 200 {object} response.Response "Success"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 404 {object} response.Response "Account not found"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/accounts/{id} [delete]
func (h *AccountHandler) DeleteAccount(c *gin.Context) {
	id, err := getUintParam(c, "id")
	if err != nil {
		response.Error(c, err)
		return
	}
	accountService, err := h.accountServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}

	ctx := c.Request.Context()
	if err := accountService.DeleteAccount(ctx, id); err != nil {
		response.Error(c, err)
		return
	}

	h.stopAccountListener(id)

	response.Success(c, nil)
}

// RestartAccount restarts an account's Discord listener without trusting client-supplied health.
// @Summary Restart account listener
// @Description Restart the specified Discord account listener. is_healthy is managed by the server and will be updated after the listener verifies the account.
// @Tags Account
// @Param id path int true "Account ID"
// @Produce json
// @Success 200 {object} response.Response{data=AccountView} "Success"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 404 {object} response.Response "Account not found"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/accounts/{id}/restart [post]
func (h *AccountHandler) RestartAccount(c *gin.Context) {
	id, err := getUintParam(c, "id")
	if err != nil {
		response.Error(c, err)
		return
	}
	accountService, err := h.accountServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}

	ctx := c.Request.Context()
	account, err := accountService.GetAccountByID(ctx, id)
	if err != nil {
		response.Error(c, err)
		return
	}
	if account == nil {
		response.Error(c, handlerInternalError("account result is required"))
		return
	}
	if account.IsDisabled {
		response.Error(c, apperrors.NewInvalidInput("account is disabled"))
		return
	}

	if err := accountService.SetAccountHealthy(ctx, id, false, ""); err != nil {
		response.Error(c, err)
		return
	}
	account.IsHealthy = false
	account.LastError = ""

	h.stopAccountListener(id)
	h.startAccountListenerAsync(account)

	response.Success(c, accountViewFromModel(account))
}

// UpdateAccount updates an account and restarts its Discord listener when needed
// @Summary Update account
// @Description Update the specified Discord account. At least one field must be provided. is_healthy is read-only and managed by listener checks; use the restart endpoint to re-check an account. While current_jobs is greater than 0, listener-changing fields (guild_id, channel_id, user_token, and disabling the account) are rejected; concurrent_limit can still be changed.
// @Tags Account
// @Param id path int true "Account ID"
// @Param request body UpdateAccountReq true "Account info"
// @Accept json
// @Produce json
// @Success 200 {object} response.Response{data=AccountView} "Success"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 404 {object} response.Response "Account not found"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/accounts/{id} [put]
func (h *AccountHandler) UpdateAccount(c *gin.Context) {
	id, err := getUintParam(c, "id")
	if err != nil {
		response.Error(c, err)
		return
	}

	var req UpdateAccountReq
	if err := bindStrictJSON(c, &req); err != nil {
		response.Error(c, err)
		return
	}
	accountService, err := h.accountServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}

	ctx := c.Request.Context()
	oldAccount, err := accountService.GetAccountByID(ctx, id)
	if err != nil {
		response.Error(c, err)
		return
	}
	if oldAccount == nil {
		response.Error(c, handlerInternalError("current account result is required"))
		return
	}

	account, err := accountService.UpdateAccount(ctx, id, &service.UpdateAccountRequest{
		GuildID:         req.GuildID,
		ChannelID:       req.ChannelID,
		UserToken:       req.UserToken,
		IsDisabled:      req.IsDisabled,
		ConcurrentLimit: req.ConcurrentLimit,
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	if account == nil {
		response.Error(c, handlerInternalError("updated account result is required"))
		return
	}

	listenerConfigChanged := oldAccount.UserToken != account.UserToken ||
		oldAccount.GuildID != account.GuildID ||
		oldAccount.ChannelID != account.ChannelID
	reactivated := oldAccount.IsDisabled && !account.IsDisabled

	if account.IsDisabled {
		h.stopAccountListener(id)
	} else if listenerConfigChanged || reactivated {
		h.stopAccountListener(id)
		h.startAccountListenerAsync(account)
	}

	response.Success(c, accountViewFromModel(account))
}

// ListAccounts returns the list of all accounts
// @Summary List accounts
// @Description Get all account list
// @Tags Account
// @Produce json
// @Success 200 {object} response.Response{data=[]AccountView} "Success"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/accounts [get]
func (h *AccountHandler) ListAccounts(c *gin.Context) {
	ctx := c.Request.Context()
	accountService, err := h.accountServiceOrError()
	if err != nil {
		response.Error(c, err)
		return
	}
	accounts, err := accountService.ListAccounts(ctx)

	if err != nil {
		response.Error(c, err)
		return
	}

	response.Success(c, accountViewsFromModels(accounts))
}

func accountViewsFromModels(accounts []model.Account) []AccountView {
	views := make([]AccountView, 0, len(accounts))
	for i := range accounts {
		views = append(views, accountViewFromModel(&accounts[i]))
	}
	return views
}

func accountViewFromModel(account *model.Account) AccountView {
	if account == nil {
		return AccountView{}
	}
	return AccountView{
		ID:              account.ID,
		GuildID:         account.GuildID,
		ChannelID:       account.ChannelID,
		IsDisabled:      account.IsDisabled,
		IsHealthy:       account.IsHealthy,
		ConcurrentLimit: account.ConcurrentLimit,
		CurrentJobs:     account.CurrentJobs,
		LastUsedAt:      account.LastUsedAt,
		LastError:       redact.Text(account.LastError),
		ErrorCount:      account.ErrorCount,
		SuccessCount:    account.SuccessCount,
		CreatedAt:       account.CreatedAt,
		UpdatedAt:       account.UpdatedAt,
	}
}

func getUintParam(c *gin.Context, param string) (uint, error) {
	str := c.Param(param)
	val, err := strconv.ParseUint(str, 10, 32)
	if err != nil || val == 0 {
		return 0, apperrors.NewInvalidInput(param + " must be a positive integer")
	}
	return uint(val), nil
}

func (h *AccountHandler) accountServiceOrError() (service.AccountService, error) {
	if h == nil || h.accountService == nil {
		return nil, handlerInternalError("account service is required")
	}
	return h.accountService, nil
}

func (h *AccountHandler) startAccountListenerAsync(account *model.Account) {
	if h.listenerManager == nil {
		return
	}

	go func() {
		if err := h.listenerManager.StartAccountListener(account); err != nil {
			// is_healthy will be updated by StartAccountListener.
			_ = err
		}
	}()
}

func (h *AccountHandler) stopAccountListener(accountID uint) {
	if h.listenerManager == nil {
		return
	}
	_ = h.listenerManager.StopAccountListener(accountID)
}
