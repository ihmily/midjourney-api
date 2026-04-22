package handler

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/service"
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

func NewAccountHandler(accountService service.AccountService, listenerManager ListenerManager) *AccountHandler {
	return &AccountHandler{
		accountService:  accountService,
		listenerManager: listenerManager,
	}
}

// CreateAccount creates a new account and starts its Discord listener
// @Summary Create account
// @Description Create a new Discord account and start its listener
// @Tags Account
// @Accept json
// @Produce json
// @Param request body service.CreateAccountRequest true "Account info"
// @Success 200 {object} response.Response "Success"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/accounts [post]
func (h *AccountHandler) CreateAccount(c *gin.Context) {
	var req service.CreateAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	ctx := context.Background()
	account, err := h.accountService.CreateAccount(ctx, &req)
	if err != nil {
		response.Error(c, err)
		return
	}

	// Start listener asynchronously (takes time to connect)
	go func() {
		if err := h.listenerManager.StartAccountListener(account); err != nil {
			// Health will be updated to UNHEALTHY inside StartAccountListener on failure
			_ = err
		}
	}()

	response.Success(c, account)
}

// DeleteAccount deletes an account and stops its Discord listener
// @Summary Delete account
// @Description Delete the specified account and stop its listener
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

	// Stop listener first (ignore error, still proceed with deletion)
	_ = h.listenerManager.StopAccountListener(id)

	ctx := context.Background()
	if err := h.accountService.DeleteAccount(ctx, id); err != nil {
		response.Error(c, err)
		return
	}

	response.Success(c, nil)
}

// ListAccounts returns the list of all accounts
// @Summary List accounts
// @Description Get all account list
// @Tags Account
// @Produce json
// @Success 200 {object} response.Response "Success"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/accounts [get]
func (h *AccountHandler) ListAccounts(c *gin.Context) {
	ctx := context.Background()
	accounts, err := h.accountService.ListAccounts(ctx)

	if err != nil {
		response.Error(c, err)
		return
	}

	response.Success(c, accounts)
}

func getUintParam(c *gin.Context, param string) (uint, error) {
	str := c.Param(param)
	val, err := strconv.ParseUint(str, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint(val), nil
}
