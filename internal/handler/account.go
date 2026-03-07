package handler

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/service"
	"github.com/trae/midjourney-api/pkg/response"
)

type UpdateAccountHealthRequest struct {
	Health string `json:"health"`
}

type AccountHandler struct {
	accountService service.AccountService
}

func NewAccountHandler(accountService service.AccountService) *AccountHandler {
	return &AccountHandler{
		accountService: accountService,
	}
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

// HealthCheckAccount checks the health status of a specific account
// @Summary Check account health
// @Description Check the health status of the specified account
// @Tags Account
// @Param id path int true "Account ID"
// @Produce json
// @Success 200 {object} response.Response "Success"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 404 {object} response.Response "Account not found"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/accounts/{id}/health [get]
func (h *AccountHandler) HealthCheckAccount(c *gin.Context) {
	ctx := context.Background()
	id, err := getUintParam(c, "id")
	if err != nil {
		response.Error(c, err)
		return
	}

	account, err := h.accountService.GetAccountByID(ctx, id)
	if err != nil {
		response.Error(c, err)
		return
	}

	isHealthy, reason := h.accountService.CheckAccountHealth(account)

	response.Success(c, map[string]interface{}{
		"account_id": id,
		"is_healthy": isHealthy,
		"reason":     reason,
		"account":    account,
	})
}

// UpdateAccountHealth updates the health status of a specific account
// @Summary Update account health
// @Description Manually update the health status of the specified account
// @Tags Account
// @Param id path int true "Account ID"
// @Param request body UpdateAccountHealthRequest true "Update request"
// @Produce json
// @Success 200 {object} response.Response "Success"
// @Failure 400 {object} response.Response "Bad request"
// @Failure 404 {object} response.Response "Account not found"
// @Failure 500 {object} response.Response "Internal server error"
// @Router /api/v1/accounts/{id}/health [put]
func (h *AccountHandler) UpdateAccountHealth(c *gin.Context) {
	ctx := context.Background()
	id, err := getUintParam(c, "id")
	if err != nil {
		response.Error(c, err)
		return
	}

	var req UpdateAccountHealthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	if err := h.accountService.UpdateAccountHealth(ctx, id, model.AccountHealth(req.Health), "manual update"); err != nil {
		response.Error(c, err)
		return
	}

	updated, err := h.accountService.GetAccountByID(ctx, id)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Success(c, updated)
}
