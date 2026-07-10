package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/pkg/response"
)

func TestTaskHandlersRejectMultipleJSONValues(t *testing.T) {
	tests := []struct {
		name   string
		target string
		body   string
		route  func(*gin.Engine, *TaskHandler)
		called func(*fakeTaskService) bool
	}{
		{
			name:   "imagine",
			target: "/tasks/imagine",
			body:   `{"prompt":"a quiet harbor"} {"prompt":"second"}`,
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/imagine", handler.CreateImagineTask)
			},
			called: func(service *fakeTaskService) bool {
				return service.imagineCalled
			},
		},
		{
			name:   "describe",
			target: "/tasks/describe",
			body:   `{"image_url":"https://example.com/image.png"} {"image_url":"https://example.com/second.png"}`,
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/describe", handler.CreateDescribeTask)
			},
			called: func(service *fakeTaskService) bool {
				return service.describeCalled
			},
		},
		{
			name:   "action",
			target: "/tasks/action",
			body:   `{"task_id":"task-1","action_type":"upscale","index":1} {"task_id":"task-2","action_type":"upscale","index":2}`,
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/action", handler.PerformTaskAction)
			},
			called: func(service *fakeTaskService) bool {
				return service.actionCalled
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			taskService := &fakeTaskService{}
			handler := NewTaskHandler(taskService)
			router := gin.New()
			tt.route(router, handler)

			recorder := performJSONRequest(router, http.MethodPost, tt.target, tt.body)

			assertBadRequestMessageContains(t, recorder, "single JSON value")
			if tt.called(taskService) {
				t.Fatal("service should not be called for request body with multiple JSON values")
			}
		})
	}
}

func TestAccountHandlersRejectMultipleJSONValues(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   string
		route  func(*gin.Engine, *AccountHandler)
		called func(*fakeAccountHandlerService) bool
	}{
		{
			name:   "create",
			method: http.MethodPost,
			target: "/accounts",
			body:   `{"guild_id":"guild-1","channel_id":"channel-1","user_token":"token"} {"guild_id":"guild-2","channel_id":"channel-2","user_token":"token"}`,
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.POST("/accounts", handler.CreateAccount)
			},
			called: func(service *fakeAccountHandlerService) bool {
				return service.createCalled
			},
		},
		{
			name:   "update",
			method: http.MethodPut,
			target: "/accounts/7",
			body:   `{"is_disabled":false} {"is_disabled":true}`,
			route: func(router *gin.Engine, handler *AccountHandler) {
				router.PUT("/accounts/:id", handler.UpdateAccount)
			},
			called: func(service *fakeAccountHandlerService) bool {
				return service.getCalled || service.updateCalled
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			accountService := &fakeAccountHandlerService{}
			handler := NewAccountHandler(accountService, nil)
			router := gin.New()
			tt.route(router, handler)

			recorder := performJSONRequest(router, tt.method, tt.target, tt.body)

			assertBadRequestMessageContains(t, recorder, "single JSON value")
			if tt.called(accountService) {
				t.Fatal("service should not be called for request body with multiple JSON values")
			}
		})
	}
}

func performJSONRequest(router *gin.Engine, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func assertBadRequestMessageContains(t *testing.T, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	var body response.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if !strings.Contains(body.Message, want) && !strings.Contains(body.Detail, want) {
		t.Fatalf("response did not contain %q: message=%q detail=%q", want, body.Message, body.Detail)
	}
}
