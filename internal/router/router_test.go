package router

import (
	"context"
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
