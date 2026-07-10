package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/service"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
)

func TestDeleteAccountStopsListenerAfterSuccessfulDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var events []string
	accountService := &fakeAccountHandlerService{events: &events}
	listenerManager := &fakeListenerManager{events: &events}
	handler := NewAccountHandler(accountService, listenerManager)
	router := gin.New()
	router.DELETE("/accounts/:id", handler.DeleteAccount)

	req := httptest.NewRequest(http.MethodDelete, "/accounts/7", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	wantEvents := []string{"delete:7", "stop:7"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestDeleteAccountDoesNotStopListenerWhenDeleteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var events []string
	accountService := &fakeAccountHandlerService{
		events:    &events,
		deleteErr: apperrors.NewDatabaseError(errors.New("delete failed")),
	}
	listenerManager := &fakeListenerManager{events: &events}
	handler := NewAccountHandler(accountService, listenerManager)
	router := gin.New()
	router.DELETE("/accounts/:id", handler.DeleteAccount)

	req := httptest.NewRequest(http.MethodDelete, "/accounts/7", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	wantEvents := []string{"delete:7"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
}

func TestDeleteAccountAllowsMissingListenerManager(t *testing.T) {
	gin.SetMode(gin.TestMode)

	accountService := &fakeAccountHandlerService{}
	handler := NewAccountHandler(accountService, nil)
	router := gin.New()
	router.DELETE("/accounts/:id", handler.DeleteAccount)

	req := httptest.NewRequest(http.MethodDelete, "/accounts/7", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestRestartAccountMarksUnhealthyAndRestartsListener(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var events []string
	started := make(chan struct{}, 1)
	accountService := &fakeAccountHandlerService{
		getAccount: &model.Account{
			ID:        7,
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserToken: "token",
			IsHealthy: true,
		},
		events: &events,
	}
	listenerManager := &fakeListenerManager{events: &events, startCh: started}
	handler := NewAccountHandler(accountService, listenerManager)
	router := gin.New()
	router.POST("/accounts/:id/restart", handler.RestartAccount)

	req := httptest.NewRequest(http.MethodPost, "/accounts/7/restart", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("listener was not started")
	}

	wantEvents := []string{"set_health:7:false", "stop:7", "start:7"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}

	var parsed struct {
		Data AccountView `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal restart response: %v", err)
	}
	if parsed.Data.IsHealthy {
		t.Fatalf("restart response is_healthy = true, want false until listener verifies")
	}
}

func TestUpdateAccountRejectsClientSuppliedHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var events []string
	accountService := &fakeAccountHandlerService{
		getAccount: &model.Account{
			ID:        7,
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserToken: "token",
			IsHealthy: false,
		},
		updatedAccount: &model.Account{
			ID:        7,
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserToken: "token",
			IsHealthy: false,
		},
	}
	listenerManager := &fakeListenerManager{events: &events}
	handler := NewAccountHandler(accountService, listenerManager)
	router := gin.New()
	router.PUT("/accounts/:id", handler.UpdateAccount)

	req := httptest.NewRequest(http.MethodPut, "/accounts/7", strings.NewReader(`{"is_healthy":true}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}

func TestUpdateAccountDoesNotTouchListenerWhenUpdateFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var events []string
	accountService := &fakeAccountHandlerService{
		getAccount: &model.Account{
			ID:        7,
			GuildID:   "guild-1",
			ChannelID: "channel-1",
			UserToken: "token",
		},
		updateErr: apperrors.NewInvalidInput("account has active tasks"),
	}
	listenerManager := &fakeListenerManager{events: &events}
	handler := NewAccountHandler(accountService, listenerManager)
	router := gin.New()
	router.PUT("/accounts/:id", handler.UpdateAccount)

	req := httptest.NewRequest(http.MethodPut, "/accounts/7", strings.NewReader(`{"user_token":"token-2"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}

func TestCreateAccountRejectsRemovedFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "concurrent limit",
			body: `{"guild_id":"guild-1","channel_id":"channel-1","user_token":"token","concurrent_limit":20}`,
		},
		{
			name: "runtime health",
			body: `{"guild_id":"guild-1","channel_id":"channel-1","user_token":"token","is_healthy":true}`,
		},
		{
			name: "old status",
			body: `{"guild_id":"guild-1","channel_id":"channel-1","user_token":"token","status":"ACTIVE"}`,
		},
		{
			name: "old health",
			body: `{"guild_id":"guild-1","channel_id":"channel-1","user_token":"token","health":"HEALTHY"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			handler := NewAccountHandler(nil, nil)
			router := gin.New()
			router.POST("/accounts", handler.CreateAccount)

			req := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}

			var body struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}
			if body.Code != string(apperrors.ErrCodeInvalidInput) {
				t.Fatalf("code = %q, want %q", body.Code, apperrors.ErrCodeInvalidInput)
			}
			if !strings.Contains(body.Detail, "unknown field") {
				t.Fatalf("detail = %q, want unknown field context", body.Detail)
			}
		})
	}
}

func TestRestartAccountRejectsDisabledAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var events []string
	accountService := &fakeAccountHandlerService{
		getAccount: &model.Account{
			ID:         7,
			GuildID:    "guild-1",
			ChannelID:  "channel-1",
			UserToken:  "token",
			IsDisabled: true,
		},
		events: &events,
	}
	listenerManager := &fakeListenerManager{events: &events}
	handler := NewAccountHandler(accountService, listenerManager)
	router := gin.New()
	router.POST("/accounts/:id/restart", handler.RestartAccount)

	req := httptest.NewRequest(http.MethodPost, "/accounts/7/restart", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}

func TestAccountHandlerRejectsInvalidIDParamBeforeService(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   string
		route  func(*gin.Engine, *AccountHandler)
	}{
		{
			name:   "delete non integer",
			method: http.MethodDelete,
			target: "/accounts/not-a-number",
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.DELETE("/accounts/:id", handler.DeleteAccount)
			},
		},
		{
			name:   "delete zero",
			method: http.MethodDelete,
			target: "/accounts/0",
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.DELETE("/accounts/:id", handler.DeleteAccount)
			},
		},
		{
			name:   "update non integer",
			method: http.MethodPut,
			target: "/accounts/not-a-number",
			body:   `{"is_disabled":false}`,
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.PUT("/accounts/:id", handler.UpdateAccount)
			},
		},
		{
			name:   "update zero",
			method: http.MethodPut,
			target: "/accounts/0",
			body:   `{"is_disabled":false}`,
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.PUT("/accounts/:id", handler.UpdateAccount)
			},
		},
		{
			name:   "restart non integer",
			method: http.MethodPost,
			target: "/accounts/not-a-number/restart",
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.POST("/accounts/:id/restart", handler.RestartAccount)
			},
		},
		{
			name:   "restart zero",
			method: http.MethodPost,
			target: "/accounts/0/restart",
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.POST("/accounts/:id/restart", handler.RestartAccount)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			handler := NewAccountHandler(nil, nil)
			router := gin.New()
			tt.route(router, handler)

			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}

			var body struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Detail  string `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}
			if body.Code != string(apperrors.ErrCodeInvalidInput) {
				t.Fatalf("code = %q, want %q", body.Code, apperrors.ErrCodeInvalidInput)
			}
			if body.Message != "id must be a positive integer" {
				t.Fatalf("message = %q, want positive integer guidance", body.Message)
			}
			if strings.Contains(body.Detail, "strconv") {
				t.Fatalf("detail exposed parser internals: %q", body.Detail)
			}
		})
	}
}

func TestListenerLifecycleMethodsAllowMissingManager(t *testing.T) {
	handler := NewAccountHandler(nil, nil)

	handler.startAccountListenerAsync(&model.Account{ID: 1})
	handler.stopAccountListener(1)
}

func TestAccountHandlerDelegatesValueValidationToService(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		target  string
		body    string
		service *fakeAccountHandlerService
		route   func(*gin.Engine, *AccountHandler)
		assert  func(*testing.T, *fakeAccountHandlerService)
	}{
		{
			name:    "create keeps raw values",
			method:  http.MethodPost,
			target:  "/accounts",
			body:    `{"guild_id":"  guild-1  ","channel_id":"  channel-1  ","user_token":"  token  "}`,
			service: &fakeAccountHandlerService{},
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.POST("/accounts", handler.CreateAccount)
			},
			assert: func(t *testing.T, service *fakeAccountHandlerService) {
				t.Helper()
				if service.lastCreateReq == nil {
					t.Fatal("CreateAccount was not called")
				}
				if service.lastCreateReq.GuildID != "  guild-1  " ||
					service.lastCreateReq.ChannelID != "  channel-1  " ||
					service.lastCreateReq.UserToken != "  token  " {
					t.Fatalf("create request = %#v, want raw values passed to service", service.lastCreateReq)
				}
			},
		},
		{
			name:   "update keeps business validation in service",
			method: http.MethodPut,
			target: "/accounts/7",
			body:   `{"guild_id":"  guild-2  ","concurrent_limit":0}`,
			service: &fakeAccountHandlerService{
				getAccount:     &model.Account{ID: 7, GuildID: "guild-1", ChannelID: "channel-1", UserToken: "token"},
				updatedAccount: &model.Account{ID: 7, GuildID: "guild-1", ChannelID: "channel-1", UserToken: "token"},
			},
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.PUT("/accounts/:id", handler.UpdateAccount)
			},
			assert: func(t *testing.T, service *fakeAccountHandlerService) {
				t.Helper()
				if service.lastUpdateReq == nil {
					t.Fatal("UpdateAccount was not called")
				}
				if service.lastUpdateReq.GuildID == nil || *service.lastUpdateReq.GuildID != "  guild-2  " {
					t.Fatalf("guild_id = %#v, want raw value passed to service", service.lastUpdateReq.GuildID)
				}
				if service.lastUpdateReq.ConcurrentLimit == nil || *service.lastUpdateReq.ConcurrentLimit != 0 {
					t.Fatalf("concurrent_limit = %#v, want service to validate zero value", service.lastUpdateReq.ConcurrentLimit)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			handler := NewAccountHandler(tt.service, nil)
			router := gin.New()
			tt.route(router, handler)

			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			tt.assert(t, tt.service)
		})
	}
}

func TestAccountHandlerResponsesUsePublicAccountView(t *testing.T) {
	gin.SetMode(gin.TestMode)

	accountService := &fakeAccountHandlerService{
		accounts: []model.Account{
			{
				ID:              7,
				GuildID:         "guild-1",
				ChannelID:       "channel-1",
				UserToken:       "secret-token",
				IsHealthy:       true,
				ConcurrentLimit: 20,
				LastError:       `discord API error: user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`,
			},
		},
	}
	handler := NewAccountHandler(accountService, nil)
	router := gin.New()
	router.GET("/accounts", handler.ListAccounts)

	req := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	body := recorder.Body.String()
	for _, forbidden := range []string{
		`"user_token":`,
		`"deleted_at"`,
		"secret-token",
		"token=secret",
		"user:pass",
		"#frag",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("account response exposed %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `user_token=\"\u003credacted\u003e\"`) ||
		!strings.Contains(body, "https://example.com/hook") {
		t.Fatalf("account response did not keep useful redacted error context: %s", body)
	}

	var parsed struct {
		Data []AccountView `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal account response: %v", err)
	}
	if len(parsed.Data) != 1 {
		t.Fatalf("accounts len = %d, want 1", len(parsed.Data))
	}
	account := parsed.Data[0]
	if account.ID != 7 || account.GuildID != "guild-1" || account.ChannelID != "channel-1" {
		t.Fatalf("account view = %#v", account)
	}
	if !account.IsHealthy || account.ConcurrentLimit != 20 {
		t.Fatalf("account health/limit = healthy %v limit %d, want true/20", account.IsHealthy, account.ConcurrentLimit)
	}
}

func TestAccountHandlerRejectsMissingService(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   string
		route  func(*gin.Engine, *AccountHandler)
	}{
		{
			name:   "create",
			method: http.MethodPost,
			target: "/accounts",
			body:   `{"guild_id":"guild-1","channel_id":"channel-1","user_token":"token"}`,
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.POST("/accounts", handler.CreateAccount)
			},
		},
		{
			name:   "list",
			method: http.MethodGet,
			target: "/accounts",
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.GET("/accounts", handler.ListAccounts)
			},
		},
		{
			name:   "update",
			method: http.MethodPut,
			target: "/accounts/7",
			body:   `{"is_disabled":false}`,
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.PUT("/accounts/:id", handler.UpdateAccount)
			},
		},
		{
			name:   "restart",
			method: http.MethodPost,
			target: "/accounts/7/restart",
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.POST("/accounts/:id/restart", handler.RestartAccount)
			},
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			target: "/accounts/7",
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.DELETE("/accounts/:id", handler.DeleteAccount)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			handler := NewAccountHandler(nil, nil)
			router := gin.New()
			tt.route(router, handler)

			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			assertInternalErrorResponse(t, recorder)
		})
	}
}

func TestAccountHandlerRejectsNilServiceResult(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		target         string
		body           string
		accountService *fakeAccountHandlerService
		route          func(*gin.Engine, *AccountHandler)
	}{
		{
			name:           "create",
			method:         http.MethodPost,
			target:         "/accounts",
			body:           `{"guild_id":"guild-1","channel_id":"channel-1","user_token":"token"}`,
			accountService: &fakeAccountHandlerService{returnNilCreate: true},
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.POST("/accounts", handler.CreateAccount)
			},
		},
		{
			name:           "update current account",
			method:         http.MethodPut,
			target:         "/accounts/7",
			body:           `{"is_disabled":false}`,
			accountService: &fakeAccountHandlerService{returnNilGet: true},
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.PUT("/accounts/:id", handler.UpdateAccount)
			},
		},
		{
			name:   "update result",
			method: http.MethodPut,
			target: "/accounts/7",
			body:   `{"is_disabled":false}`,
			accountService: &fakeAccountHandlerService{
				getAccount:      &model.Account{ID: 7, GuildID: "guild-1", ChannelID: "channel-1", UserToken: "token"},
				returnNilUpdate: true,
			},
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.PUT("/accounts/:id", handler.UpdateAccount)
			},
		},
		{
			name:           "restart current account",
			method:         http.MethodPost,
			target:         "/accounts/7/restart",
			accountService: &fakeAccountHandlerService{returnNilGet: true},
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.POST("/accounts/:id/restart", handler.RestartAccount)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			handler := NewAccountHandler(tt.accountService, nil)
			router := gin.New()
			tt.route(router, handler)

			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			assertInternalErrorResponse(t, recorder)
		})
	}
}

type fakeListenerManager struct {
	events  *[]string
	startCh chan struct{}
}

func (m *fakeListenerManager) StartAccountListener(account *model.Account) error {
	if m.events != nil && account != nil {
		*m.events = append(*m.events, fmt.Sprintf("start:%d", account.ID))
	}
	if m.startCh != nil {
		select {
		case m.startCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (m *fakeListenerManager) StopAccountListener(accountID uint) error {
	if m.events != nil {
		*m.events = append(*m.events, fmt.Sprintf("stop:%d", accountID))
	}
	return nil
}

type fakeAccountHandlerService struct {
	events          *[]string
	createCalled    bool
	getCalled       bool
	updateCalled    bool
	lastCreateReq   *service.CreateAccountRequest
	lastUpdateReq   *service.UpdateAccountRequest
	deleteErr       error
	updateErr       error
	getAccount      *model.Account
	updatedAccount  *model.Account
	accounts        []model.Account
	returnNilCreate bool
	returnNilGet    bool
	returnNilUpdate bool
}

func (s *fakeAccountHandlerService) CreateAccount(ctx context.Context, req *service.CreateAccountRequest) (*model.Account, error) {
	s.createCalled = true
	s.lastCreateReq = req
	if s.returnNilCreate {
		return nil, nil
	}
	return &model.Account{}, nil
}

func (s *fakeAccountHandlerService) GetAccountByID(ctx context.Context, id uint) (*model.Account, error) {
	s.getCalled = true
	if s.returnNilGet {
		return nil, nil
	}
	if s.getAccount != nil {
		account := *s.getAccount
		return &account, nil
	}
	return &model.Account{ID: id}, nil
}

func (s *fakeAccountHandlerService) ListAccounts(ctx context.Context) ([]model.Account, error) {
	accounts := make([]model.Account, len(s.accounts))
	copy(accounts, s.accounts)
	return accounts, nil
}

func (s *fakeAccountHandlerService) UpdateAccount(ctx context.Context, id uint, req *service.UpdateAccountRequest) (*model.Account, error) {
	s.updateCalled = true
	s.lastUpdateReq = req
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.returnNilUpdate {
		return nil, nil
	}
	if s.updatedAccount != nil {
		account := *s.updatedAccount
		return &account, nil
	}
	return &model.Account{ID: id}, nil
}

func (s *fakeAccountHandlerService) DeleteAccount(ctx context.Context, id uint) error {
	if s.events != nil {
		*s.events = append(*s.events, fmt.Sprintf("delete:%d", id))
	}
	return s.deleteErr
}

func (s *fakeAccountHandlerService) AcquireAvailableAccount(ctx context.Context) (*model.Account, error) {
	return nil, nil
}

func (s *fakeAccountHandlerService) AcquireAccount(ctx context.Context, id uint) (*model.Account, error) {
	return nil, nil
}

func (s *fakeAccountHandlerService) SetAccountHealthy(ctx context.Context, id uint, isHealthy bool, lastError string) error {
	if s.events != nil {
		*s.events = append(*s.events, fmt.Sprintf("set_health:%d:%t", id, isHealthy))
	}
	return nil
}

func (s *fakeAccountHandlerService) DecrementJobs(ctx context.Context, id uint) error {
	return nil
}

func (s *fakeAccountHandlerService) RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error {
	return nil
}
