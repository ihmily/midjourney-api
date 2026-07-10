package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/internal/service"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestStopCancelsWorkerContext(t *testing.T) {
	w := NewWorker(nil, nil, "queue", time.Second, zap.NewNop())

	w.Stop()
	w.Stop()

	select {
	case <-w.ctx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("worker context was not canceled")
	}
}

func TestWorkerRunClosesDoneForWait(t *testing.T) {
	w := NewWorker(nil, nil, "queue", time.Second, zap.NewNop())
	done := make(chan struct{})

	go func() {
		_ = w.Run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker Run did not return for missing dependencies")
	}
	if !w.Wait(time.Second) {
		t.Fatal("Wait returned false after worker Run exited")
	}
}

func TestWorkerWaitTimesOutWhileRunning(t *testing.T) {
	w := NewWorker(nil, nil, "queue", time.Second, zap.NewNop())
	w.running.Store(true)
	w.started.Store(true)
	defer w.running.Store(false)

	if w.Wait(10 * time.Millisecond) {
		t.Fatal("Wait returned true for running worker with open done channel")
	}
}

func TestWorkerWaitTracksStartedWorkerUntilDone(t *testing.T) {
	w := NewWorker(nil, nil, "queue", time.Second, zap.NewNop())
	w.markStarted()
	w.running.Store(false)

	if w.Wait(10 * time.Millisecond) {
		t.Fatal("Wait returned true for a started worker before done was closed")
	}

	w.doneOnce.Do(func() {
		close(w.done)
	})
	if !w.Wait(time.Second) {
		t.Fatal("Wait returned false after done was closed")
	}
}

func TestWorkerWaitReturnsTrueBeforeStart(t *testing.T) {
	w := NewWorker(nil, nil, "queue", time.Second, zap.NewNop())

	if !w.Wait(10 * time.Millisecond) {
		t.Fatal("Wait returned false before worker was started")
	}
}

func TestNewWorkerDefaultsLoggerAndTimeout(t *testing.T) {
	w := NewWorker(nil, nil, "queue", 0, nil)

	if w.logger == nil {
		t.Fatal("logger was nil")
	}
	if w.timeout <= 0 {
		t.Fatalf("timeout = %s, want positive default", w.timeout)
	}
	if w.queueName != "queue" {
		t.Fatalf("queueName = %q, want queue", w.queueName)
	}
}

func TestWorkerStartReturnsWhenDependenciesMissing(t *testing.T) {
	w := NewWorker(nil, nil, "queue", time.Second, nil)
	done := make(chan struct{})

	go func() {
		w.start()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not return when dependencies were missing")
	}
}

func TestWorkerStartReturnsWhenQueueNameMissing(t *testing.T) {
	w := NewWorker(&fakeWorkerTaskService{}, &redis.Client{}, "   ", time.Second, nil)
	done := make(chan struct{})

	go func() {
		w.start()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not return when queue name was missing")
	}
}

func TestNilWorkerStopIsNoop(t *testing.T) {
	var w *Worker
	w.Stop()
}

func TestZeroValueWorkerStopAndRunAreSafe(t *testing.T) {
	w := &Worker{}

	w.Stop()
	if err := w.Run(); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !w.Wait(time.Second) {
		t.Fatal("zero value worker did not report stopped")
	}
}

func TestStartWorkersRejectsNonPositiveCount(t *testing.T) {
	if workers := StartWorkers(0, nil, nil, "queue", time.Second, nil); len(workers) != 0 {
		t.Fatalf("workers len = %d, want 0", len(workers))
	}
	if workers := StartWorkers(-1, nil, nil, "queue", time.Second, nil); len(workers) != 0 {
		t.Fatalf("workers len = %d, want 0", len(workers))
	}
}

func TestStartWorkersRejectsInvalidStartupInputs(t *testing.T) {
	taskService := &fakeWorkerTaskService{}
	redisClient := &redis.Client{}

	tests := []struct {
		name        string
		taskService service.TaskService
		redisClient *redis.Client
		queueName   string
	}{
		{
			name:        "missing task service",
			redisClient: redisClient,
			queueName:   "queue",
		},
		{
			name:        "missing redis client",
			taskService: taskService,
			queueName:   "queue",
		},
		{
			name:        "missing queue name",
			taskService: taskService,
			redisClient: redisClient,
			queueName:   "   ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workers := StartWorkers(2, tt.taskService, tt.redisClient, tt.queueName, time.Second, zap.NewNop())
			if len(workers) != 0 {
				t.Fatalf("workers len = %d, want 0", len(workers))
			}
		})
	}
}

func TestQueueMessageKindFromJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		want service.QueueMessageKind
	}{
		{
			name: "imagine",
			data: `{"kind":"imagine","task_id":"task-1","prompt":"a harbor"}`,
			want: service.QueueMessageKindImagine,
		},
		{
			name: "trimmed kind",
			data: `{"kind":" imagine ","task_id":"task-1","prompt":"a harbor"}`,
			want: service.QueueMessageKindImagine,
		},
		{
			name: "describe",
			data: `{"kind":"describe","task_id":"task-2","image_url":"https://example.com/image.png"}`,
			want: service.QueueMessageKindDescribe,
		},
		{
			name: "action",
			data: `{"kind":"action","task_id":"task-3","custom_id":"MJ::JOB::upsample::1::uuid"}`,
			want: service.QueueMessageKindAction,
		},
		{
			name: "missing kind",
			data: `{"task_id":"old-task","prompt":"old prompt"}`,
			want: "",
		},
		{
			name: "unknown kind",
			data: `{"kind":"unknown","task_id":"task-4"}`,
			want: service.QueueMessageKind("unknown"),
		},
		{
			name: "invalid json",
			data: `{`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := service.QueueMessageKindFromJSON(tt.data); got != tt.want {
				t.Fatalf("QueueMessageKindFromJSON = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRejectUnknownQueueMessageRejectsIdentifiableMessage(t *testing.T) {
	taskService := &fakeWorkerTaskService{}
	w := NewWorker(taskService, nil, "queue", time.Second, zap.NewNop())

	w.rejectUnknownQueueMessage(`{"kind":"legacy","task_id":" task-1 ","account_id":9}`)

	if taskService.rejectedTaskID != "task-1" {
		t.Fatalf("rejected task_id = %q, want task-1", taskService.rejectedTaskID)
	}
	if taskService.rejectedAccountID != 9 {
		t.Fatalf("rejected account_id = %d, want 9", taskService.rejectedAccountID)
	}
	if taskService.rejectedReason == nil {
		t.Fatal("rejected reason was nil")
	}
}

func TestRejectQueueMessageSalvagesTaskIDFromPartiallyInvalidMessage(t *testing.T) {
	tests := []struct {
		name          string
		run           func(*Worker, context.Context)
		wantTaskID    string
		wantAccountID uint
	}{
		{
			name: "bad account id type",
			run: func(w *Worker, ctx context.Context) {
				w.processImagineTask(ctx, `{"kind":"imagine","task_id":" task-bad-account ","account_id":{"bad":true},"prompt":123}`)
			},
			wantTaskID: "task-bad-account",
		},
		{
			name: "account id string",
			run: func(w *Worker, ctx context.Context) {
				w.processImagineTask(ctx, `{"kind":"imagine","task_id":" task-string-account ","account_id":"9","prompt":123}`)
			},
			wantTaskID:    "task-string-account",
			wantAccountID: 9,
		},
		{
			name: "multiple json values",
			run: func(w *Worker, ctx context.Context) {
				w.processImagineTask(ctx, `{"kind":"imagine","task_id":" task-multiple ","account_id":7,"prompt":"a harbor"} {"kind":"imagine","task_id":"second","account_id":7,"prompt":"second"}`)
			},
			wantTaskID:    "task-multiple",
			wantAccountID: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskService := &fakeWorkerTaskService{}
			w := NewWorker(taskService, nil, "queue", time.Second, zap.NewNop())

			tt.run(w, context.Background())

			if taskService.rejectedCount != 1 {
				t.Fatalf("rejectedCount = %d, want 1", taskService.rejectedCount)
			}
			if taskService.rejectedTaskID != tt.wantTaskID {
				t.Fatalf("rejected task_id = %q, want %q", taskService.rejectedTaskID, tt.wantTaskID)
			}
			if taskService.rejectedAccountID != tt.wantAccountID {
				t.Fatalf("rejected account_id = %d, want %d", taskService.rejectedAccountID, tt.wantAccountID)
			}
			if taskService.rejectedReason == nil {
				t.Fatal("rejected reason was nil")
			}
		})
	}
}

func TestProcessKnownQueueMessageRejectsIdentifiableDecodeError(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Worker, context.Context)
	}{
		{
			name: "imagine",
			run: func(w *Worker, ctx context.Context) {
				w.processImagineTask(ctx, `{"kind":"imagine","task_id":" task-1 ","account_id":9,"prompt":123}`)
			},
		},
		{
			name: "describe",
			run: func(w *Worker, ctx context.Context) {
				w.processDescribeTask(ctx, `{"kind":"describe","task_id":" task-1 ","account_id":9,"image_url":123}`)
			},
		},
		{
			name: "action",
			run: func(w *Worker, ctx context.Context) {
				w.processActionTask(ctx, `{"kind":"action","task_id":" task-1 ","account_id":9,"custom_id":123,"discord_message_id":"message-1"}`)
			},
		},
		{
			name: "legacy imagine credentials",
			run: func(w *Worker, ctx context.Context) {
				w.processImagineTask(ctx, `{"kind":"imagine","task_id":" task-1 ","account_id":9,"prompt":"a harbor","user_token":"old-token"}`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskService := &fakeWorkerTaskService{}
			w := NewWorker(taskService, nil, "queue", time.Second, zap.NewNop())

			tt.run(w, context.Background())

			if taskService.rejectedCount != 1 {
				t.Fatalf("rejectedCount = %d, want 1", taskService.rejectedCount)
			}
			if taskService.rejectedTaskID != "task-1" {
				t.Fatalf("rejected task_id = %q, want task-1", taskService.rejectedTaskID)
			}
			if taskService.rejectedAccountID != 9 {
				t.Fatalf("rejected account_id = %d, want 9", taskService.rejectedAccountID)
			}
			if taskService.rejectedReason == nil {
				t.Fatal("rejected reason was nil")
			}
		})
	}
}

func TestProcessKnownQueueMessageDoesNotRejectUnidentifiableDecodeError(t *testing.T) {
	taskService := &fakeWorkerTaskService{}
	w := NewWorker(taskService, nil, "queue", time.Second, zap.NewNop())

	w.processImagineTask(context.Background(), `{`)

	if taskService.rejectedCount != 0 {
		t.Fatalf("rejectedCount = %d, want 0", taskService.rejectedCount)
	}
}

func TestProcessQueueMessageUsesFreshTaskTimeout(t *testing.T) {
	taskService := &fakeWorkerTaskService{}
	timeout := 250 * time.Millisecond
	w := NewWorker(taskService, nil, "queue", timeout, zap.NewNop())

	start := time.Now()
	w.processQueueMessage(`{"kind":"imagine","task_id":"task-1","account_id":9,"prompt":"a harbor"}`)

	if taskService.processTaskCalls != 1 {
		t.Fatalf("processTaskCalls = %d, want 1", taskService.processTaskCalls)
	}
	if !taskService.processTaskDeadlineSet {
		t.Fatal("process task context had no deadline")
	}
	minDeadline := start.Add(timeout / 2)
	maxDeadline := start.Add(timeout + 150*time.Millisecond)
	if taskService.processTaskDeadline.Before(minDeadline) || taskService.processTaskDeadline.After(maxDeadline) {
		t.Fatalf("process task deadline = %s, want around %s", taskService.processTaskDeadline.Sub(start), timeout)
	}
}

func TestDecodeQueueMessagesOnlyRejectMalformedJSON(t *testing.T) {
	tests := []struct {
		name    string
		decode  func(string) error
		data    string
		wantErr bool
	}{
		{
			name: "valid imagine",
			decode: func(data string) error {
				_, err := service.DecodeImagineTaskMessage(data)
				return err
			},
			data: `{"kind":"imagine","task_id":"task-1","account_id":1,"prompt":"a harbor"}`,
		},
		{
			name: "imagine missing prompt reaches service validation",
			decode: func(data string) error {
				_, err := service.DecodeImagineTaskMessage(data)
				return err
			},
			data: `{"kind":"imagine","task_id":"task-1","account_id":1}`,
		},
		{
			name: "imagine blank prompt reaches service validation",
			decode: func(data string) error {
				_, err := service.DecodeImagineTaskMessage(data)
				return err
			},
			data: `{"kind":"imagine","task_id":"task-1","account_id":1,"prompt":"   "}`,
		},
		{
			name: "valid describe",
			decode: func(data string) error {
				_, err := service.DecodeDescribeTaskMessage(data)
				return err
			},
			data: `{"kind":"describe","task_id":"task-2","account_id":2,"image_url":"https://example.com/image.png"}`,
		},
		{
			name: "describe missing account reaches service validation",
			decode: func(data string) error {
				_, err := service.DecodeDescribeTaskMessage(data)
				return err
			},
			data: `{"kind":"describe","task_id":"task-2","image_url":"https://example.com/image.png"}`,
		},
		{
			name: "describe invalid image URL reaches service validation",
			decode: func(data string) error {
				_, err := service.DecodeDescribeTaskMessage(data)
				return err
			},
			data: `{"kind":"describe","task_id":"task-2","account_id":2,"image_url":"ftp://example.com/image.png"}`,
		},
		{
			name: "valid action",
			decode: func(data string) error {
				_, err := service.DecodeActionTaskMessage(data)
				return err
			},
			data: `{"kind":"action","task_id":"task-3","account_id":3,"custom_id":"MJ::JOB::upsample::1::uuid","discord_message_id":"message-1"}`,
		},
		{
			name: "action missing discord message reaches service validation",
			decode: func(data string) error {
				_, err := service.DecodeActionTaskMessage(data)
				return err
			},
			data: `{"kind":"action","task_id":"task-3","account_id":3,"custom_id":"MJ::JOB::upsample::1::uuid"}`,
		},
		{
			name: "action blank custom id reaches service validation",
			decode: func(data string) error {
				_, err := service.DecodeActionTaskMessage(data)
				return err
			},
			data: `{"kind":"action","task_id":"task-3","account_id":3,"custom_id":"   ","discord_message_id":"message-1"}`,
		},
		{
			name: "malformed imagine json",
			decode: func(data string) error {
				_, err := service.DecodeImagineTaskMessage(data)
				return err
			},
			data:    `{`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.decode(tt.data)
			if tt.wantErr && err == nil {
				t.Fatalf("decode returned nil error, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("decode returned error: %v", err)
			}
		})
	}
}

func TestDecodeQueueMessagesRejectUnknownFields(t *testing.T) {
	tests := []struct {
		name   string
		decode func(string) error
		data   string
	}{
		{
			name: "imagine legacy credentials",
			decode: func(data string) error {
				_, err := service.DecodeImagineTaskMessage(data)
				return err
			},
			data: `{"kind":"imagine","task_id":"task-1","account_id":1,"prompt":"a harbor","user_token":"old-token","guild_id":"old-guild","channel_id":"old-channel"}`,
		},
		{
			name: "describe legacy callback",
			decode: func(data string) error {
				_, err := service.DecodeDescribeTaskMessage(data)
				return err
			},
			data: `{"kind":"describe","task_id":"task-2","account_id":2,"image_url":"https://example.com/image.png","callback_url":"https://example.com/hook"}`,
		},
		{
			name: "action unknown field",
			decode: func(data string) error {
				_, err := service.DecodeActionTaskMessage(data)
				return err
			},
			data: `{"kind":"action","task_id":"task-3","parent_task_id":"parent","account_id":3,"custom_id":"custom","discord_message_id":"message","unexpected":"value"}`,
		},
		{
			name: "multiple json values",
			decode: func(data string) error {
				_, err := service.DecodeImagineTaskMessage(data)
				return err
			},
			data: `{"kind":"imagine","task_id":"task-1","account_id":1,"prompt":"a harbor"} {"kind":"imagine","task_id":"task-2","account_id":1,"prompt":"second"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.decode(tt.data); err == nil {
				t.Fatal("decode returned nil error, want strict decode error")
			}
		})
	}
}

func TestDecodeQueueMessagesRejectWrongKind(t *testing.T) {
	tests := []struct {
		name   string
		decode func(string) error
		data   string
	}{
		{
			name: "imagine decoder rejects describe kind",
			decode: func(data string) error {
				_, err := service.DecodeImagineTaskMessage(data)
				return err
			},
			data: `{"kind":"describe","task_id":"task-1","account_id":1,"prompt":"a harbor"}`,
		},
		{
			name: "describe decoder rejects action kind",
			decode: func(data string) error {
				_, err := service.DecodeDescribeTaskMessage(data)
				return err
			},
			data: `{"kind":"action","task_id":"task-2","account_id":2,"image_url":"https://example.com/image.png"}`,
		},
		{
			name: "action decoder rejects imagine kind",
			decode: func(data string) error {
				_, err := service.DecodeActionTaskMessage(data)
				return err
			},
			data: `{"kind":"imagine","task_id":"task-3","account_id":3,"custom_id":"custom","discord_message_id":"message"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.decode(tt.data); err == nil {
				t.Fatal("decode returned nil error, want wrong-kind error")
			}
		})
	}
}

func TestWorkerRedactsProcessingErrorLogs(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	taskService := &fakeWorkerTaskService{
		processTaskErr: errors.New(`discord API error: user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`),
	}
	w := NewWorker(taskService, nil, "queue", time.Second, zap.New(core))

	w.processImagineTask(context.Background(), `{"kind":"imagine","task_id":"task-1","account_id":9,"prompt":"a harbor"}`)

	entries := logs.FilterMessage("Failed to process task").All()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	errorValue, ok := entries[0].ContextMap()["error"].(string)
	if !ok {
		t.Fatalf("log error field type = %T, want string", entries[0].ContextMap()["error"])
	}
	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(errorValue, forbidden) {
			t.Fatalf("worker log exposed %q: %s", forbidden, errorValue)
		}
	}
	if !strings.Contains(errorValue, `user_token="<redacted>"`) || !strings.Contains(errorValue, "https://example.com/hook") {
		t.Fatalf("worker log did not keep useful redacted context: %s", errorValue)
	}
}

func TestWorkerDoesNotLogPromptOrCustomID(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	taskService := &fakeWorkerTaskService{}
	w := NewWorker(taskService, nil, "queue", time.Second, zap.New(core))

	w.processImagineTask(context.Background(), `{"kind":"imagine","task_id":"task-1","account_id":9,"prompt":"secret prompt text"}`)
	w.processActionTask(context.Background(), `{"kind":"action","task_id":"task-2","parent_task_id":"parent-1","account_id":9,"custom_id":"secret-custom-id","discord_message_id":"message-1"}`)

	logText := logs.AllUntimed()
	rendered := fmt.Sprint(logText)
	for _, forbidden := range []string{"secret prompt text", "secret-custom-id", "custom_id"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("worker logs exposed %q: %s", forbidden, rendered)
		}
	}

	processingLogs := logs.FilterMessage("Processing task").All()
	if len(processingLogs) != 1 {
		t.Fatalf("processing task logs = %d, want 1", len(processingLogs))
	}
	if _, ok := processingLogs[0].ContextMap()["prompt_length"]; !ok {
		t.Fatalf("processing task log missing prompt_length: %#v", processingLogs[0].ContextMap())
	}
	if _, ok := processingLogs[0].ContextMap()["prompt"]; ok {
		t.Fatalf("processing task log should not contain prompt: %#v", processingLogs[0].ContextMap())
	}
}

func TestWorkerPanicLogTextIsRedacted(t *testing.T) {
	panicValue := panicLogText(`user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`)

	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(panicValue, forbidden) {
			t.Fatalf("worker panic log exposed %q: %s", forbidden, panicValue)
		}
	}
	if !strings.Contains(panicValue, `user_token="<redacted>"`) || !strings.Contains(panicValue, "https://example.com/hook") {
		t.Fatalf("worker panic log did not keep useful redacted context: %s", panicValue)
	}
}

func TestRedactedErrorFieldRedactsSecrets(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	logger := zap.New(core)

	logger.Error("redacted", redactedErrorField(errors.New(
		`redis failed password=secret-password callback=https://user:pass@example.com/hook?token=secret#frag`,
	)))
	logger.Error("nil", redactedErrorField(nil))

	entry := logs.FilterMessage("redacted").All()[0]
	errorValue, ok := entry.ContextMap()["error"].(string)
	if !ok {
		t.Fatalf("error field type = %T, want string", entry.ContextMap()["error"])
	}
	for _, forbidden := range []string{"secret-password", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(errorValue, forbidden) {
			t.Fatalf("redacted error field exposed %q: %s", forbidden, errorValue)
		}
	}
	if !strings.Contains(errorValue, "password=<redacted>") || !strings.Contains(errorValue, "https://example.com/hook") {
		t.Fatalf("redacted error field did not keep useful context: %s", errorValue)
	}

	nilEntry := logs.FilterMessage("nil").All()[0]
	if _, ok := nilEntry.ContextMap()["error"]; ok {
		t.Fatalf("nil error should not log error field: %#v", nilEntry.ContextMap())
	}
}

type fakeWorkerTaskService struct {
	rejectedTaskID         string
	rejectedAccountID      uint
	rejectedReason         error
	rejectedCount          int
	processTaskErr         error
	processTaskCalls       int
	processTaskDeadline    time.Time
	processTaskDeadlineSet bool
}

func (s *fakeWorkerTaskService) CreateImagineTask(ctx context.Context, req *service.CreateTaskRequest) (*service.TaskResponse, error) {
	return nil, nil
}

func (s *fakeWorkerTaskService) CreateDescribeTask(ctx context.Context, req *service.CreateDescribeTaskRequest) (*service.TaskResponse, error) {
	return nil, nil
}

func (s *fakeWorkerTaskService) GetTask(ctx context.Context, taskID string) (*model.Task, error) {
	return nil, nil
}

func (s *fakeWorkerTaskService) ListTasks(ctx context.Context, limit, offset int) ([]model.Task, int64, error) {
	return nil, 0, nil
}

func (s *fakeWorkerTaskService) ProcessTask(ctx context.Context, msg *service.TaskMessage) error {
	s.processTaskCalls++
	s.processTaskDeadline, s.processTaskDeadlineSet = ctx.Deadline()
	return s.processTaskErr
}

func (s *fakeWorkerTaskService) ProcessDescribeTask(ctx context.Context, msg *service.TaskDescribeMessage) error {
	return nil
}

func (s *fakeWorkerTaskService) GetQueueList(ctx context.Context) (*service.QueueStatus, error) {
	return nil, nil
}

func (s *fakeWorkerTaskService) PerformTaskAction(ctx context.Context, req *service.TaskActionRequest) (*service.TaskResponse, error) {
	return nil, nil
}

func (s *fakeWorkerTaskService) ProcessActionTask(ctx context.Context, msg *service.TaskActionMessage) error {
	return nil
}

func (s *fakeWorkerTaskService) RejectQueueMessage(taskID string, accountID uint, reason error) error {
	s.rejectedTaskID = taskID
	s.rejectedAccountID = accountID
	s.rejectedReason = reason
	s.rejectedCount++
	return nil
}

func (s *fakeWorkerTaskService) SweepTimedOutTasks(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	return 0, nil
}

func TestDecodeQueueMessagesTrimFields(t *testing.T) {
	imagine, err := service.DecodeImagineTaskMessage(`{"kind":"imagine","task_id":" task-1 ","account_id":1,"prompt":" a harbor "}`)
	if err != nil {
		t.Fatalf("decodeImagineTaskMessage returned error: %v", err)
	}
	if imagine.TaskID != "task-1" || imagine.Prompt != "a harbor" {
		t.Fatalf("imagine message was not trimmed: %#v", imagine)
	}

	describe, err := service.DecodeDescribeTaskMessage(`{"kind":"describe","task_id":" task-2 ","account_id":2,"image_url":" https://example.com/image.png "}`)
	if err != nil {
		t.Fatalf("decodeDescribeTaskMessage returned error: %v", err)
	}
	if describe.TaskID != "task-2" || describe.ImageURL != "https://example.com/image.png" {
		t.Fatalf("describe message was not trimmed: %#v", describe)
	}

	action, err := service.DecodeActionTaskMessage(`{"kind":"action","task_id":" task-3 ","parent_task_id":" parent ","account_id":3,"custom_id":" custom ","discord_message_id":" message-1 "}`)
	if err != nil {
		t.Fatalf("decodeActionTaskMessage returned error: %v", err)
	}
	if action.TaskID != "task-3" ||
		action.ParentTaskID != "parent" ||
		action.CustomID != "custom" ||
		action.DiscordMessageID != "message-1" {
		t.Fatalf("action message was not trimmed: %#v", action)
	}
}
