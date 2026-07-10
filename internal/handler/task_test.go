package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/service"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"github.com/trae/midjourney-api/pkg/response"
)

func TestPerformTaskActionRejectsUnsupportedActionType(t *testing.T) {
	gin.SetMode(gin.TestMode)

	taskService := &fakeTaskService{
		actionErr: apperrors.NewInvalidInput("unsupported action_type"),
	}
	handler := NewTaskHandler(taskService)
	router := gin.New()
	router.POST("/tasks/action", handler.PerformTaskAction)

	req := httptest.NewRequest(http.MethodPost, "/tasks/action", strings.NewReader(`{
		"task_id": "task-1",
		"action_type": "invalid",
		"index": 1
	}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !taskService.actionCalled {
		t.Fatal("PerformTaskAction service should validate action_type")
	}

	var body response.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if body.Code != string(apperrors.ErrCodeInvalidInput) {
		t.Fatalf("code = %q, want %q", body.Code, apperrors.ErrCodeInvalidInput)
	}
}

func TestCreateImagineTaskRejectsInvalidCallbackURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	taskService := &fakeTaskService{
		imagineErr: apperrors.NewInvalidInput("callback_url must use http or https"),
	}
	handler := NewTaskHandler(taskService)
	router := gin.New()
	router.POST("/tasks/imagine", handler.CreateImagineTask)

	req := httptest.NewRequest(http.MethodPost, "/tasks/imagine", strings.NewReader(`{
		"prompt": "a quiet harbor",
		"callback_url": "not-a-url"
	}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !taskService.imagineCalled {
		t.Fatal("CreateImagineTask service should validate callback_url")
	}
}

func TestCreateDescribeTaskRejectsInvalidImageURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	taskService := &fakeTaskService{
		describeErr: apperrors.NewInvalidInput("image_url must use http or https"),
	}
	handler := NewTaskHandler(taskService)
	router := gin.New()
	router.POST("/tasks/describe", handler.CreateDescribeTask)

	req := httptest.NewRequest(http.MethodPost, "/tasks/describe", strings.NewReader(`{
		"image_url": "not-a-url"
	}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !taskService.describeCalled {
		t.Fatal("CreateDescribeTask service should validate image_url")
	}
}

func TestTaskViewJSONDoesNotExposeInternalTaskFields(t *testing.T) {
	accountID := uint(7)
	buttons := `["secret-button-id"]`
	task := &model.Task{
		ID:               42,
		TaskID:           "task-public",
		AccountID:        &accountID,
		ParentTaskID:     "parent-task",
		Type:             model.TaskTypeImagine,
		Prompt:           "a quiet harbor",
		Status:           model.TaskStatusFailed,
		Progress:         45,
		DiscordMessageID: "message-secret",
		ImageURL:         "https://example.com/image.png",
		OSSImageURL:      "https://oss.example.com/image.png",
		ErrorMessage:     `visible failure user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`,
		Buttons:          &buttons,
		Description:      "description text",
		CallbackURL:      "https://callback.example.com/hook?token=secret",
		CreatedAt:        time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 1, 2, 3, 5, 5, 0, time.UTC),
	}

	bodyBytes, err := json.Marshal(TaskListResponse{
		Tasks: taskViewsFromModels([]model.Task{*task}),
		Total: 1,
	})
	if err != nil {
		t.Fatalf("marshal task view: %v", err)
	}
	body := string(bodyBytes)

	for _, forbidden := range []string{
		`"id":`,
		`"account_id"`,
		"discord_message_id",
		"buttons",
		"callback_url",
		"message-secret",
		"secret-button-id",
		"callback.example.com",
		"secret-token",
		"token=secret",
		"user:pass",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("task view JSON exposed %q: %s", forbidden, body)
		}
	}
	for _, expected := range []string{"task-public", "parent-task", "visible failure", "description text"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("task view JSON omitted %q: %s", expected, body)
		}
	}
}

func TestPerformTaskActionRejectsInvalidCallbackURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	taskService := &fakeTaskService{
		actionErr: apperrors.NewInvalidInput("callback_url must use http or https"),
	}
	handler := NewTaskHandler(taskService)
	router := gin.New()
	router.POST("/tasks/action", handler.PerformTaskAction)

	req := httptest.NewRequest(http.MethodPost, "/tasks/action", strings.NewReader(`{
		"task_id": "task-1",
		"action_type": "upscale",
		"index": 1,
		"callback_url": "not-a-url"
	}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !taskService.actionCalled {
		t.Fatal("PerformTaskAction service should validate callback_url")
	}
}

func TestPerformTaskActionRejectsMissingIndexBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)

	taskService := &fakeTaskService{}
	handler := NewTaskHandler(taskService)
	router := gin.New()
	router.POST("/tasks/action", handler.PerformTaskAction)

	req := httptest.NewRequest(http.MethodPost, "/tasks/action", strings.NewReader(`{
		"task_id": "task-1",
		"action_type": "upscale"
	}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if taskService.actionCalled {
		t.Fatal("PerformTaskAction service should not be called when index is missing")
	}
}

func TestTaskHandlerDelegatesTrimmedValueValidationToService(t *testing.T) {
	tests := []struct {
		name   string
		target string
		body   string
		route  func(*gin.Engine, *TaskHandler)
		assert func(*testing.T, *fakeTaskService)
	}{
		{
			name:   "imagine callback",
			target: "/tasks/imagine",
			body:   `{"prompt":" a quiet harbor ","callback_url":"  https://callback.example.com/hook  "}`,
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/imagine", handler.CreateImagineTask)
			},
			assert: func(t *testing.T, taskService *fakeTaskService) {
				t.Helper()
				if taskService.lastImagineReq == nil {
					t.Fatal("CreateImagineTask was not called")
				}
				if taskService.lastImagineReq.CallbackURL != "  https://callback.example.com/hook  " {
					t.Fatalf("callback_url = %q, want raw value for service validation", taskService.lastImagineReq.CallbackURL)
				}
			},
		},
		{
			name:   "describe image",
			target: "/tasks/describe",
			body:   `{"image_url":"  https://example.com/image.png  ","callback_url":"  https://callback.example.com/hook  "}`,
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/describe", handler.CreateDescribeTask)
			},
			assert: func(t *testing.T, taskService *fakeTaskService) {
				t.Helper()
				if taskService.lastDescribeReq == nil {
					t.Fatal("CreateDescribeTask was not called")
				}
				if taskService.lastDescribeReq.ImageURL != "  https://example.com/image.png  " {
					t.Fatalf("image_url = %q, want raw value for service validation", taskService.lastDescribeReq.ImageURL)
				}
			},
		},
		{
			name:   "action fields",
			target: "/tasks/action",
			body:   `{"task_id":" parent-task ","action_type":"  upscale  ","index":0,"callback_url":"  https://callback.example.com/hook  "}`,
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/action", handler.PerformTaskAction)
			},
			assert: func(t *testing.T, taskService *fakeTaskService) {
				t.Helper()
				if taskService.lastActionReq == nil {
					t.Fatal("PerformTaskAction was not called")
				}
				if taskService.lastActionReq.ActionType != "  upscale  " {
					t.Fatalf("action_type = %q, want raw value for service validation", taskService.lastActionReq.ActionType)
				}
				if taskService.lastActionReq.Index != 0 {
					t.Fatalf("index = %d, want service to validate zero value", taskService.lastActionReq.Index)
				}
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

			if recorder.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
			}
			tt.assert(t, taskService)
		})
	}
}

func TestListTasksRejectsInvalidPagination(t *testing.T) {
	tests := []string{
		"/tasks?limit=bad",
		"/tasks?limit=0",
		"/tasks?offset=bad",
		"/tasks?offset=-1",
	}

	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			taskService := &fakeTaskService{}
			handler := NewTaskHandler(taskService)
			router := gin.New()
			router.GET("/tasks", handler.ListTasks)

			req := httptest.NewRequest(http.MethodGet, target, nil)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
			if taskService.listCalled {
				t.Fatal("ListTasks service should not be called for invalid pagination")
			}

			var body response.Response
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("failed to unmarshal response: %v", err)
			}
			if body.Code != string(apperrors.ErrCodeInvalidInput) {
				t.Fatalf("code = %q, want %q", body.Code, apperrors.ErrCodeInvalidInput)
			}
		})
	}
}

func TestListTasksClampsLimitAndKeepsOffset(t *testing.T) {
	gin.SetMode(gin.TestMode)

	taskService := &fakeTaskService{}
	handler := NewTaskHandler(taskService)
	router := gin.New()
	router.GET("/tasks", handler.ListTasks)

	req := httptest.NewRequest(http.MethodGet, "/tasks?limit=500&offset=7", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !taskService.listCalled {
		t.Fatal("ListTasks service was not called")
	}
	if taskService.listLimit != constants.MaxListLimit {
		t.Fatalf("limit = %d, want %d", taskService.listLimit, constants.MaxListLimit)
	}
	if taskService.listOffset != 7 {
		t.Fatalf("offset = %d, want 7", taskService.listOffset)
	}
}

func TestTaskHandlerRejectsMissingService(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   string
		route  func(*gin.Engine, *TaskHandler)
	}{
		{
			name:   "create imagine",
			method: http.MethodPost,
			target: "/tasks/imagine",
			body:   `{"prompt":"a quiet harbor"}`,
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/imagine", handler.CreateImagineTask)
			},
		},
		{
			name:   "create describe",
			method: http.MethodPost,
			target: "/tasks/describe",
			body:   `{"image_url":"https://example.com/image.png"}`,
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/describe", handler.CreateDescribeTask)
			},
		},
		{
			name:   "get task",
			method: http.MethodGet,
			target: "/tasks/task-1",
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.GET("/tasks/:task_id", handler.GetTask)
			},
		},
		{
			name:   "list tasks",
			method: http.MethodGet,
			target: "/tasks",
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.GET("/tasks", handler.ListTasks)
			},
		},
		{
			name:   "queue",
			method: http.MethodGet,
			target: "/tasks/queue",
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.GET("/tasks/queue", handler.GetQueueList)
			},
		},
		{
			name:   "action",
			method: http.MethodPost,
			target: "/tasks/action",
			body:   `{"task_id":"task-1","action_type":"upscale","index":1}`,
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/action", handler.PerformTaskAction)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			handler := NewTaskHandler(nil)
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

func TestTaskHandlerRejectsNilServiceResult(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		target      string
		body        string
		taskService *fakeTaskService
		route       func(*gin.Engine, *TaskHandler)
	}{
		{
			name:        "create imagine",
			method:      http.MethodPost,
			target:      "/tasks/imagine",
			body:        `{"prompt":"a quiet harbor"}`,
			taskService: &fakeTaskService{returnNilImagine: true},
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/imagine", handler.CreateImagineTask)
			},
		},
		{
			name:        "create describe",
			method:      http.MethodPost,
			target:      "/tasks/describe",
			body:        `{"image_url":"https://example.com/image.png"}`,
			taskService: &fakeTaskService{returnNilDescribe: true},
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/describe", handler.CreateDescribeTask)
			},
		},
		{
			name:        "get task",
			method:      http.MethodGet,
			target:      "/tasks/task-1",
			taskService: &fakeTaskService{returnNilTask: true},
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.GET("/tasks/:task_id", handler.GetTask)
			},
		},
		{
			name:        "queue",
			method:      http.MethodGet,
			target:      "/tasks/queue",
			taskService: &fakeTaskService{returnNilQueue: true},
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.GET("/tasks/queue", handler.GetQueueList)
			},
		},
		{
			name:        "action",
			method:      http.MethodPost,
			target:      "/tasks/action",
			body:        `{"task_id":"task-1","action_type":"upscale","index":1}`,
			taskService: &fakeTaskService{returnNilAction: true},
			route: func(router *gin.Engine, handler *TaskHandler) {
				router.POST("/tasks/action", handler.PerformTaskAction)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)

			handler := NewTaskHandler(tt.taskService)
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

func assertInternalErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	var body response.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if body.Code != string(apperrors.ErrCodeInternal) {
		t.Fatalf("code = %q, want %q", body.Code, apperrors.ErrCodeInternal)
	}
	if body.Detail != "" {
		t.Fatalf("detail = %q, want empty", body.Detail)
	}
}

type fakeTaskService struct {
	imagineCalled     bool
	describeCalled    bool
	actionCalled      bool
	listCalled        bool
	listLimit         int
	listOffset        int
	imagineErr        error
	describeErr       error
	actionErr         error
	lastImagineReq    *service.CreateTaskRequest
	lastDescribeReq   *service.CreateDescribeTaskRequest
	lastActionReq     *service.TaskActionRequest
	returnNilImagine  bool
	returnNilDescribe bool
	returnNilTask     bool
	returnNilQueue    bool
	returnNilAction   bool
}

func (s *fakeTaskService) CreateImagineTask(ctx context.Context, req *service.CreateTaskRequest) (*service.TaskResponse, error) {
	s.imagineCalled = true
	s.lastImagineReq = req
	if s.imagineErr != nil {
		return nil, s.imagineErr
	}
	if s.returnNilImagine {
		return nil, nil
	}
	return &service.TaskResponse{}, nil
}

func (s *fakeTaskService) CreateDescribeTask(ctx context.Context, req *service.CreateDescribeTaskRequest) (*service.TaskResponse, error) {
	s.describeCalled = true
	s.lastDescribeReq = req
	if s.describeErr != nil {
		return nil, s.describeErr
	}
	if s.returnNilDescribe {
		return nil, nil
	}
	return &service.TaskResponse{}, nil
}

func (s *fakeTaskService) GetTask(ctx context.Context, taskID string) (*model.Task, error) {
	if s.returnNilTask {
		return nil, nil
	}
	return &model.Task{TaskID: taskID}, nil
}

func (s *fakeTaskService) ListTasks(ctx context.Context, limit, offset int) ([]model.Task, int64, error) {
	s.listCalled = true
	s.listLimit = limit
	s.listOffset = offset
	return nil, 0, nil
}

func (s *fakeTaskService) ProcessTask(ctx context.Context, msg *service.TaskMessage) error {
	return nil
}

func (s *fakeTaskService) ProcessDescribeTask(ctx context.Context, msg *service.TaskDescribeMessage) error {
	return nil
}

func (s *fakeTaskService) GetQueueList(ctx context.Context) (*service.QueueStatus, error) {
	if s.returnNilQueue {
		return nil, nil
	}
	return &service.QueueStatus{}, nil
}

func (s *fakeTaskService) PerformTaskAction(ctx context.Context, req *service.TaskActionRequest) (*service.TaskResponse, error) {
	s.actionCalled = true
	s.lastActionReq = req
	if s.actionErr != nil {
		return nil, s.actionErr
	}
	if s.returnNilAction {
		return nil, nil
	}
	return &service.TaskResponse{}, nil
}

func (s *fakeTaskService) ProcessActionTask(ctx context.Context, msg *service.TaskActionMessage) error {
	return nil
}

func (s *fakeTaskService) RejectQueueMessage(taskID string, accountID uint, reason error) error {
	return nil
}

func (s *fakeTaskService) SweepTimedOutTasks(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	return 0, nil
}
