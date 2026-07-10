package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/internal/handler"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/service"
)

func TestSetupRoutesQueueBeforeTaskID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	taskService := &fakeRouterTaskService{}
	engine := gin.New()
	Setup(
		engine,
		handler.NewTaskHandler(taskService),
		handler.NewAccountHandler(nil, nil),
		handler.NewHealthHandler(),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/queue", nil)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !taskService.queueCalled {
		t.Fatal("queue handler was not called")
	}
	if taskService.getTaskCalled {
		t.Fatal("task detail handler should not handle /api/v1/tasks/queue")
	}
}

func TestSwaggerDocUsesRequestHost(t *testing.T) {
	engine := setupRouterTestEngine()

	req := httptest.NewRequest(http.MethodGet, "http://203.0.113.10:8080/swagger/doc.json", nil)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	doc := decodeSwaggerDoc(t, recorder.Body.Bytes())
	if got := doc["host"]; got != "203.0.113.10:8080" {
		t.Fatalf("host = %v, want %q", got, "203.0.113.10:8080")
	}
	assertSwaggerSchemes(t, doc, "http")
}

func TestSwaggerDocUsesForwardedHostAndProto(t *testing.T) {
	engine := setupRouterTestEngine()

	req := httptest.NewRequest(http.MethodGet, "http://10.0.0.10:8080/swagger/doc.json", nil)
	req.Header.Set("X-Forwarded-Host", "api.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	doc := decodeSwaggerDoc(t, recorder.Body.Bytes())
	if got := doc["host"]; got != "api.example.com" {
		t.Fatalf("host = %v, want %q", got, "api.example.com")
	}
	assertSwaggerSchemes(t, doc, "https")
}

func setupRouterTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	Setup(
		engine,
		handler.NewTaskHandler(&fakeRouterTaskService{}),
		handler.NewAccountHandler(nil, nil),
		handler.NewHealthHandler(),
	)
	return engine
}

func decodeSwaggerDoc(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode swagger doc: %v", err)
	}
	return doc
}

func assertSwaggerSchemes(t *testing.T, doc map[string]any, want string) {
	t.Helper()

	schemes, ok := doc["schemes"].([]any)
	if !ok {
		t.Fatalf("schemes has type %T, want array", doc["schemes"])
	}
	if len(schemes) != 1 || schemes[0] != want {
		t.Fatalf("schemes = %#v, want [%q]", schemes, want)
	}
}

type fakeRouterTaskService struct {
	queueCalled   bool
	getTaskCalled bool
}

func (s *fakeRouterTaskService) CreateImagineTask(ctx context.Context, req *service.CreateTaskRequest) (*service.TaskResponse, error) {
	return &service.TaskResponse{}, nil
}

func (s *fakeRouterTaskService) CreateDescribeTask(ctx context.Context, req *service.CreateDescribeTaskRequest) (*service.TaskResponse, error) {
	return &service.TaskResponse{}, nil
}

func (s *fakeRouterTaskService) GetTask(ctx context.Context, taskID string) (*model.Task, error) {
	s.getTaskCalled = true
	return &model.Task{TaskID: taskID}, nil
}

func (s *fakeRouterTaskService) ListTasks(ctx context.Context, limit, offset int) ([]model.Task, int64, error) {
	return nil, 0, nil
}

func (s *fakeRouterTaskService) ProcessTask(ctx context.Context, msg *service.TaskMessage) error {
	return nil
}

func (s *fakeRouterTaskService) ProcessDescribeTask(ctx context.Context, msg *service.TaskDescribeMessage) error {
	return nil
}

func (s *fakeRouterTaskService) GetQueueList(ctx context.Context) (*service.QueueStatus, error) {
	s.queueCalled = true
	return &service.QueueStatus{}, nil
}

func (s *fakeRouterTaskService) PerformTaskAction(ctx context.Context, req *service.TaskActionRequest) (*service.TaskResponse, error) {
	return &service.TaskResponse{}, nil
}

func (s *fakeRouterTaskService) ProcessActionTask(ctx context.Context, msg *service.TaskActionMessage) error {
	return nil
}

func (s *fakeRouterTaskService) RejectQueueMessage(taskID string, accountID uint, reason error) error {
	return nil
}

func (s *fakeRouterTaskService) SweepTimedOutTasks(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	return 0, nil
}
