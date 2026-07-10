package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/internal/discord"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"go.uber.org/zap"
)

func TestParseQueueItemSupportsAllMessageKinds(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		wantKind QueueMessageKind
		wantID   string
	}{
		{
			name:     "imagine",
			data:     `{"kind":"imagine","task_id":"imagine-task","prompt":"a quiet harbor","account_id":1}`,
			wantKind: QueueMessageKindImagine,
			wantID:   "imagine-task",
		},
		{
			name:     "describe",
			data:     `{"kind":"describe","task_id":"describe-task","image_url":"https://example.com/image.png","account_id":2}`,
			wantKind: QueueMessageKindDescribe,
			wantID:   "describe-task",
		},
		{
			name:     "action",
			data:     `{"kind":"action","task_id":"action-task","custom_id":"MJ::JOB::upsample::1::uuid","discord_message_id":"message-1","account_id":3}`,
			wantKind: QueueMessageKindAction,
			wantID:   "action-task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item, ok := parseQueueItem(tt.data)
			if !ok {
				t.Fatalf("parseQueueItem returned false")
			}
			if item.Kind != tt.wantKind {
				t.Fatalf("kind = %q, want %q", item.Kind, tt.wantKind)
			}
			if item.TaskID != tt.wantID {
				t.Fatalf("task_id = %q, want %q", item.TaskID, tt.wantID)
			}

			itemJSON := mustMarshalString(t, item)
			if strings.Contains(itemJSON, "secret-token") || strings.Contains(itemJSON, "user_token") || strings.Contains(itemJSON, "account_id") {
				t.Fatalf("queue item exposed user token: %s", itemJSON)
			}
		})
	}
}

func TestParseQueueItemRejectsLegacyCallbackURL(t *testing.T) {
	item, ok := parseQueueItem(`{"kind":"imagine","task_id":"task-callback","account_id":1,"prompt":"a harbor","callback_url":"https://callback.example.com/hook?token=secret-token"}`)
	if ok {
		t.Fatalf("parseQueueItem = %#v, true; want false for legacy callback_url", item)
	}
}

func TestParseQueueItemRejectsStrictDecodeFailures(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "legacy credentials",
			data: `{"kind":"imagine","task_id":"task-legacy","account_id":1,"prompt":"a harbor","user_token":"secret-token","guild_id":"old-guild","channel_id":"old-channel"}`,
		},
		{
			name: "unknown action field",
			data: `{"kind":"action","task_id":"task-action","parent_task_id":"parent","account_id":3,"custom_id":"custom","discord_message_id":"message","unexpected":"value"}`,
		},
		{
			name: "multiple json values",
			data: `{"kind":"describe","task_id":"task-describe","account_id":2,"image_url":"https://example.com/image.png"} {"kind":"describe","task_id":"second","account_id":2,"image_url":"https://example.com/second.png"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if item, ok := parseQueueItem(tt.data); ok {
				t.Fatalf("parseQueueItem = %#v, true; want false", item)
			}
		})
	}
}

func TestQueueItemJSONDoesNotExposeInternalActionFields(t *testing.T) {
	data := `{"kind":"action","task_id":"action-task","parent_task_id":"parent-task","custom_id":"secret-custom-id","discord_message_id":"message-secret","account_id":3}`

	item, ok := parseQueueItem(data)
	if !ok {
		t.Fatal("parseQueueItem returned false")
	}

	body := mustMarshalString(t, item)
	for _, forbidden := range []string{"account_id", "custom_id", "discord_message_id", "secret-custom-id", "message-secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("queue item JSON exposed %q: %s", forbidden, body)
		}
	}
	for _, expected := range []string{"action-task", "parent-task"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("queue item JSON omitted %q: %s", expected, body)
		}
	}
}

func TestProcessingQueueItemsDoNotExposeInternalTaskFields(t *testing.T) {
	accountID := uint(7)
	buttons := mustMarshalString(t, []string{"secret-button-id"})
	task := &model.Task{
		ID:               42,
		TaskID:           "task-processing",
		AccountID:        &accountID,
		ParentTaskID:     "parent-task",
		Type:             model.TaskTypeImagine,
		Prompt:           "a quiet harbor",
		Status:           model.TaskStatusProcessing,
		Progress:         45,
		DiscordMessageID: "message-secret",
		ImageURL:         "https://example.com/image.png",
		OSSImageURL:      "https://oss.example.com/image.png",
		ErrorMessage:     "transient secret error",
		Buttons:          &buttons,
		Description:      "description text",
		CallbackURL:      "https://callback.example.com/hook?token=secret",
		CreatedAt:        time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 1, 2, 3, 5, 5, 0, time.UTC),
	}

	items := processingQueueItems([]*model.Task{nil, task})
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].TaskID != task.TaskID || items[0].AccountID == nil || *items[0].AccountID != accountID {
		t.Fatalf("processing item = %#v, want task/account ids copied", items[0])
	}

	body := mustMarshalString(t, QueueStatus{ProcessingTasks: items})
	for _, forbidden := range []string{
		"callback_url",
		"account_id",
		"discord_message_id",
		"buttons",
		"message-secret",
		"secret-button-id",
		"callback.example.com",
		"transient secret error",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("processing queue JSON exposed %q: %s", forbidden, body)
		}
	}
	for _, expected := range []string{"task-processing", "PROCESSING", "a quiet harbor", "description text"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("processing queue JSON omitted %q: %s", expected, body)
		}
	}
}

func TestQueueMessagesDoNotContainAccountCredentials(t *testing.T) {
	messages := []any{
		TaskMessage{
			Kind:      QueueMessageKindImagine,
			TaskID:    "imagine-task",
			Prompt:    "a quiet harbor",
			AccountID: 1,
		},
		TaskDescribeMessage{
			Kind:      QueueMessageKindDescribe,
			TaskID:    "describe-task",
			ImageURL:  "https://example.com/image.png",
			AccountID: 2,
		},
		TaskActionMessage{
			Kind:      QueueMessageKindAction,
			TaskID:    "action-task",
			CustomID:  "MJ::JOB::upsample::1::uuid",
			AccountID: 3,
		},
	}

	for _, msg := range messages {
		body := mustMarshalString(t, msg)
		for _, forbidden := range []string{"user_token", "guild_id", "channel_id", "callback_url", "secret-token"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("queue message exposed %q: %s", forbidden, body)
			}
		}
	}
}

func TestParseQueueItemRejectsMessageWithoutKind(t *testing.T) {
	data := `{"task_id":"old-task","prompt":"old prompt","guild_id":"guild","channel_id":"channel","user_token":"secret-token","account_id":9}`

	if item, ok := parseQueueItem(data); ok {
		t.Fatalf("parseQueueItem = %#v, true; want false for message without kind", item)
	}
}

func TestQueueInspectRange(t *testing.T) {
	tests := []struct {
		name      string
		length    int64
		limit     int64
		wantStart int64
		wantStop  int64
		wantCount int
	}{
		{
			name:      "empty queue",
			length:    0,
			limit:     100,
			wantStart: 0,
			wantStop:  -1,
			wantCount: 0,
		},
		{
			name:      "short queue",
			length:    3,
			limit:     100,
			wantStart: 0,
			wantStop:  2,
			wantCount: 3,
		},
		{
			name:      "long queue keeps tail closest to BRPOP",
			length:    250,
			limit:     100,
			wantStart: 150,
			wantStop:  249,
			wantCount: 100,
		},
		{
			name:      "non-positive limit",
			length:    10,
			limit:     0,
			wantStart: 0,
			wantStop:  -1,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, stop, count := queueInspectRange(tt.length, tt.limit)
			if start != tt.wantStart || stop != tt.wantStop || count != tt.wantCount {
				t.Fatalf("queueInspectRange = (%d, %d, %d), want (%d, %d, %d)",
					start, stop, count, tt.wantStart, tt.wantStop, tt.wantCount)
			}
		})
	}
}

func TestCreateQueuedTaskRollsBackWhenEnqueueFails(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		logger:         zap.NewNop(),
	}

	account := &model.Account{ID: 7}
	task := &model.Task{TaskID: "task-1", Status: model.TaskStatusPending}
	resp, err := svc.createQueuedTask(context.Background(), task, account, func(context.Context) error {
		return errors.New("redis unavailable")
	}, "imagine")

	if err == nil {
		t.Fatalf("expected enqueue error")
	}
	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	if taskRepo.createCount != 1 {
		t.Fatalf("createCount = %d, want 1", taskRepo.createCount)
	}
	rolledBack := taskRepo.tasks["task-1"]
	if rolledBack == nil {
		t.Fatalf("rolled back task was not persisted")
	}
	if rolledBack.Status != model.TaskStatusFailed {
		t.Fatalf("status = %q, want %q", rolledBack.Status, model.TaskStatusFailed)
	}
	if rolledBack.ErrorMessage != "redis unavailable" {
		t.Fatalf("error message = %q, want enqueue failure", rolledBack.ErrorMessage)
	}
	if rolledBack.FinishedAt == nil {
		t.Fatalf("finished_at was not set")
	}
	if !accountService.decremented(7) {
		t.Fatalf("expected account 7 slot to be released")
	}
}

func TestCreateQueuedTaskDoesNotReleaseSlotWhenRollbackTerminalUpdateSkipped(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	taskRepo.terminalTransitioned = false
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		logger:         zap.NewNop(),
	}

	resp, err := svc.createQueuedTask(
		context.Background(),
		&model.Task{TaskID: "task-terminal-race", Status: model.TaskStatusPending},
		&model.Account{ID: 7},
		func(context.Context) error {
			return errors.New("redis unavailable")
		},
		"imagine",
	)

	if err == nil {
		t.Fatalf("expected enqueue error")
	}
	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	if len(accountService.decrementedIDs) != 0 {
		t.Fatalf("decrementedIDs = %#v, want no release when terminal update is skipped", accountService.decrementedIDs)
	}
}

func TestCreateQueuedTaskDoesNotReleaseSlotWhenRollbackTerminalUpdateFails(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	taskRepo.updateErr = errors.New("database unavailable")
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		logger:         zap.NewNop(),
	}

	resp, err := svc.createQueuedTask(
		context.Background(),
		&model.Task{TaskID: "task-rollback-fails", Status: model.TaskStatusPending},
		&model.Account{ID: 7},
		func(context.Context) error {
			return errors.New("redis unavailable")
		},
		"imagine",
	)

	if err == nil {
		t.Fatalf("expected enqueue error")
	}
	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	if len(accountService.decrementedIDs) != 0 {
		t.Fatalf("decrementedIDs = %#v, want no release when rollback terminal update fails", accountService.decrementedIDs)
	}
}

func TestCreateQueuedTaskReleasesSlotWhenCreateFails(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	taskRepo.createErr = errors.New("database unavailable")
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		logger:         zap.NewNop(),
	}

	account := &model.Account{ID: 8}
	resp, err := svc.createQueuedTask(context.Background(), &model.Task{TaskID: "task-2"}, account, func(context.Context) error {
		t.Fatalf("enqueue should not run when create fails")
		return nil
	}, "describe")

	if err == nil {
		t.Fatalf("expected create error")
	}
	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	if !accountService.decremented(8) {
		t.Fatalf("expected account 8 slot to be released")
	}
	if _, ok := taskRepo.statusUpdates["task-2"]; ok {
		t.Fatalf("task status should not be updated when create fails")
	}
}

func TestCreateQueuedTaskReleasesSlotWhenRepositoryMissing(t *testing.T) {
	accountService := &fakeAccountService{}
	svc := &taskService{
		accountService: accountService,
		logger:         zap.NewNop(),
	}

	enqueueCalled := false
	resp, err := svc.createQueuedTask(
		context.Background(),
		&model.Task{TaskID: "task-missing-repo"},
		&model.Account{ID: 6},
		func(context.Context) error {
			enqueueCalled = true
			return nil
		},
		"imagine",
	)

	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	assertAppErrorCode(t, err, apperrors.ErrCodeInternal)
	if enqueueCalled {
		t.Fatal("enqueue should not run when repository is missing")
	}
	if !accountService.decremented(6) {
		t.Fatalf("expected account 6 slot to be released")
	}
}

func TestCreateQueuedTaskRejectsMissingInternalInputs(t *testing.T) {
	tests := []struct {
		name          string
		task          *model.Task
		account       *model.Account
		enqueue       func(context.Context) error
		wantCode      apperrors.ErrorCode
		wantDecrement bool
	}{
		{
			name:     "nil task",
			account:  &model.Account{ID: 7},
			enqueue:  func(context.Context) error { return nil },
			wantCode: apperrors.ErrCodeInvalidInput,
		},
		{
			name:     "nil account",
			task:     &model.Task{TaskID: "task-missing-account"},
			enqueue:  func(context.Context) error { return nil },
			wantCode: apperrors.ErrCodeInvalidInput,
		},
		{
			name:          "nil enqueue",
			task:          &model.Task{TaskID: "task-missing-enqueue"},
			account:       &model.Account{ID: 9},
			wantCode:      apperrors.ErrCodeInternal,
			wantDecrement: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskRepo := newFakeTaskRepo()
			accountService := &fakeAccountService{}
			svc := &taskService{
				taskRepo:       taskRepo,
				accountService: accountService,
				logger:         zap.NewNop(),
			}

			resp, err := svc.createQueuedTask(context.Background(), tt.task, tt.account, tt.enqueue, "imagine")

			if resp != nil {
				t.Fatalf("response = %#v, want nil", resp)
			}
			assertAppErrorCode(t, err, tt.wantCode)
			if taskRepo.createCount != 0 {
				t.Fatalf("createCount = %d, want 0", taskRepo.createCount)
			}
			if got := accountService.decremented(9); got != tt.wantDecrement {
				t.Fatalf("account 9 decremented = %v, want %v", got, tt.wantDecrement)
			}
		})
	}
}

func TestCreateTaskPreservesAcquireAccountAppError(t *testing.T) {
	acquireErr := apperrors.NewDatabaseError(errors.New("database unavailable"))

	tests := []struct {
		name string
		run  func(*taskService) (*TaskResponse, error)
	}{
		{
			name: "imagine",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt: "a quiet harbor",
				})
			},
		},
		{
			name: "describe",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL: "https://example.com/image.png",
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &taskService{
				taskRepo: newFakeTaskRepo(),
				accountService: &fakeAccountService{
					acquireAvailableErr: acquireErr,
				},
				logger: zap.NewNop(),
			}

			resp, err := tt.run(svc)

			if resp != nil {
				t.Fatalf("response = %#v, want nil", resp)
			}
			assertAppErrorCode(t, err, apperrors.ErrCodeDatabaseError)
		})
	}
}

func TestNewTaskServiceDefaultsLoggerAndHandlesMissingQueueConfig(t *testing.T) {
	svc, ok := NewTaskService(nil, nil, nil, nil, nil, nil).(*taskService)
	if !ok {
		t.Fatalf("NewTaskService returned %T, want *taskService", svc)
	}
	if svc.logger == nil {
		t.Fatal("logger was nil")
	}
	if _, err := svc.queueName(); err == nil {
		t.Fatal("queueName returned nil error for missing task config")
	}
	if got := svc.maxRetries(); got != 0 {
		t.Fatalf("maxRetries = %d, want 0 for missing task config", got)
	}
}

func TestTaskServiceRejectsMissingCoreDependencies(t *testing.T) {
	buttons := mustMarshalString(t, []string{"MJ::JOB::upsample::1::button-id"})
	accountID := uint(7)
	parentTask := &model.Task{
		TaskID:           "parent-task",
		AccountID:        &accountID,
		Type:             model.TaskTypeImagine,
		Prompt:           "a quiet harbor",
		Status:           model.TaskStatusSuccess,
		DiscordMessageID: "message-1",
		Buttons:          &buttons,
	}

	tests := []struct {
		name string
		svc  *taskService
		run  func(*taskService) error
	}{
		{
			name: "get task missing repository",
			svc:  &taskService{},
			run: func(svc *taskService) error {
				_, err := svc.GetTask(context.Background(), "task-1")
				return err
			},
		},
		{
			name: "list tasks missing repository",
			svc:  &taskService{},
			run: func(svc *taskService) error {
				_, _, err := svc.ListTasks(context.Background(), 10, 0)
				return err
			},
		},
		{
			name: "create imagine missing repository",
			svc: &taskService{
				accountService: &fakeAccountService{acquireAvailable: &model.Account{ID: 1}},
			},
			run: func(svc *taskService) error {
				_, err := svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt: "a quiet harbor",
				})
				return err
			},
		},
		{
			name: "create imagine missing account service",
			svc: &taskService{
				taskRepo: newFakeTaskRepo(),
			},
			run: func(svc *taskService) error {
				_, err := svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt: "a quiet harbor",
				})
				return err
			},
		},
		{
			name: "perform action missing account service",
			svc: &taskService{
				taskRepo: seededTaskRepo(parentTask),
			},
			run: func(svc *taskService) error {
				_, err := svc.PerformTaskAction(context.Background(), &TaskActionRequest{
					TaskID:     "parent-task",
					ActionType: "upscale",
					Index:      1,
				})
				return err
			},
		},
		{
			name: "process imagine missing discord client",
			svc: &taskService{
				taskRepo:       newFakeTaskRepo(),
				accountService: &fakeAccountService{},
			},
			run: func(svc *taskService) error {
				return svc.ProcessTask(context.Background(), &TaskMessage{
					TaskID:    "task-1",
					Prompt:    "a quiet harbor",
					AccountID: 1,
				})
			},
		},
		{
			name: "process describe missing discord client",
			svc: &taskService{
				taskRepo:       newFakeTaskRepo(),
				accountService: &fakeAccountService{},
			},
			run: func(svc *taskService) error {
				return svc.ProcessDescribeTask(context.Background(), &TaskDescribeMessage{
					TaskID:    "task-1",
					ImageURL:  "https://example.com/image.png",
					AccountID: 1,
				})
			},
		},
		{
			name: "process action missing discord client",
			svc: &taskService{
				taskRepo:       newFakeTaskRepo(),
				accountService: &fakeAccountService{},
			},
			run: func(svc *taskService) error {
				return svc.ProcessActionTask(context.Background(), &TaskActionMessage{
					TaskID:           "task-1",
					CustomID:         "MJ::JOB::upsample::1::button-id",
					DiscordMessageID: "message-1",
					AccountID:        1,
				})
			},
		},
		{
			name: "reject queue message missing repository",
			svc:  &taskService{},
			run: func(svc *taskService) error {
				return svc.RejectQueueMessage("task-1", 1, errors.New("bad queue message"))
			},
		},
		{
			name: "sweep timed out tasks missing repository",
			svc:  &taskService{},
			run: func(svc *taskService) error {
				_, err := svc.SweepTimedOutTasks(context.Background(), time.Now(), 1)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertAppErrorCode(t, tt.run(tt.svc), apperrors.ErrCodeInternal)
		})
	}
}

func TestProcessTaskMissingAccountServiceFailsTaskWithoutPanic(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	svc := &taskService{
		taskRepo: taskRepo,
		discord:  &fakeDiscordAPI{},
		logger:   zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-missing-account-service",
		Prompt:    "a quiet harbor",
		AccountID: 1,
	})

	assertAppErrorCode(t, err, apperrors.ErrCodeInternal)
	if got := taskRepo.statusUpdates["task-missing-account-service"]; got != model.TaskStatusProcessing {
		t.Fatalf("status update = %q, want PROCESSING", got)
	}
	task := taskRepo.tasks["task-missing-account-service"]
	if task == nil || task.Status != model.TaskStatusFailed {
		t.Fatalf("task after failure = %#v, want FAILED", task)
	}
}

func TestProcessQueuedTasksMissingDiscordClientFailsTaskAndReleasesSlot(t *testing.T) {
	accountID := uint(15)
	tests := []struct {
		name   string
		taskID string
		run    func(context.Context, *taskService, string) error
	}{
		{
			name:   "imagine",
			taskID: "task-missing-discord-imagine",
			run: func(ctx context.Context, svc *taskService, taskID string) error {
				return svc.ProcessTask(ctx, &TaskMessage{
					TaskID:    taskID,
					Prompt:    "a quiet harbor",
					AccountID: accountID,
				})
			},
		},
		{
			name:   "describe",
			taskID: "task-missing-discord-describe",
			run: func(ctx context.Context, svc *taskService, taskID string) error {
				return svc.ProcessDescribeTask(ctx, &TaskDescribeMessage{
					TaskID:    taskID,
					ImageURL:  "https://example.com/image.png",
					AccountID: accountID,
				})
			},
		},
		{
			name:   "action",
			taskID: "task-missing-discord-action",
			run: func(ctx context.Context, svc *taskService, taskID string) error {
				return svc.ProcessActionTask(ctx, &TaskActionMessage{
					TaskID:           taskID,
					CustomID:         "MJ::JOB::upsample::1::button-id",
					DiscordMessageID: "message-1",
					AccountID:        accountID,
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskRepo := newFakeTaskRepo()
			taskRepo.tasks[tt.taskID] = &model.Task{
				TaskID:    tt.taskID,
				Status:    model.TaskStatusPending,
				AccountID: &accountID,
			}
			accountService := &fakeAccountService{}
			svc := &taskService{
				taskRepo:       taskRepo,
				accountService: accountService,
				logger:         zap.NewNop(),
			}

			err := tt.run(context.Background(), svc, tt.taskID)

			assertAppErrorCode(t, err, apperrors.ErrCodeInternal)
			task := taskRepo.tasks[tt.taskID]
			if task == nil || task.Status != model.TaskStatusFailed {
				t.Fatalf("task after failure = %#v, want FAILED", task)
			}
			if !accountService.decremented(accountID) {
				t.Fatalf("expected account %d slot to be released", accountID)
			}
			if len(accountService.recordedResults) != 1 {
				t.Fatalf("recordedResults len = %d, want 1", len(accountService.recordedResults))
			}
			recorded := accountService.recordedResults[0]
			if recorded.accountID != accountID || recorded.success {
				t.Fatalf("recorded result = %#v, want failure for account %d", recorded, accountID)
			}
		})
	}
}

func TestCreateTaskMissingRedisRollsBackAccountSlot(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	accountService := &fakeAccountService{
		acquireAvailable: &model.Account{ID: 12},
	}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		taskConfig:     &config.TaskConfig{QueueName: "queue"},
	}

	resp, err := svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
		Prompt: "a quiet harbor",
	})

	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	assertAppErrorCode(t, err, apperrors.ErrCodeTaskCreateFailed)
	if !accountService.decremented(12) {
		t.Fatalf("expected acquired account slot to be released")
	}
	if taskRepo.lastCreated == nil {
		t.Fatalf("task was not created before enqueue failure")
	}
	if task := taskRepo.tasks[taskRepo.lastCreated.TaskID]; task == nil || task.Status != model.TaskStatusFailed {
		t.Fatalf("task status after rollback = %#v, want FAILED", task)
	}
}

func TestCreateTaskRejectsBlankInputBeforeAcquiringAccount(t *testing.T) {
	tests := []struct {
		name string
		run  func(*taskService) (*TaskResponse, error)
	}{
		{
			name: "blank imagine prompt",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt: "   ",
				})
			},
		},
		{
			name: "blank imagine callback",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt:      "a quiet harbor",
					CallbackURL: "   ",
				})
			},
		},
		{
			name: "invalid imagine callback scheme",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt:      "a quiet harbor",
					CallbackURL: "ftp://callback.example.com/hook",
				})
			},
		},
		{
			name: "blank describe image URL",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL: "   ",
				})
			},
		},
		{
			name: "invalid describe image URL",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL: "not-a-url",
				})
			},
		},
		{
			name: "blank describe callback",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL:    "https://example.com/image.png",
					CallbackURL: "   ",
				})
			},
		},
		{
			name: "invalid describe callback scheme",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL:    "https://example.com/image.png",
					CallbackURL: "file:///tmp/callback",
				})
			},
		},
		{
			name: "imagine callback userinfo",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt:      "a quiet harbor",
					CallbackURL: "https://user:pass@callback.example.com/hook",
				})
			},
		},
		{
			name: "imagine callback private ip literal",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt:      "a quiet harbor",
					CallbackURL: "http://127.0.0.1/hook?token=secret",
				})
			},
		},
		{
			name: "imagine callback invalid port",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt:      "a quiet harbor",
					CallbackURL: "https://callback.example.com:0/hook",
				})
			},
		},
		{
			name: "describe image userinfo",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL: "https://user:pass@example.com/image.png",
				})
			},
		},
		{
			name: "describe image private ip literal",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL: "http://10.0.0.8/image.png",
				})
			},
		},
		{
			name: "describe image invalid port",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL: "https://example.com:65536/image.png",
				})
			},
		},
		{
			name: "describe callback userinfo",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL:    "https://example.com/image.png",
					CallbackURL: "https://user:pass@callback.example.com/hook",
				})
			},
		},
		{
			name: "describe callback private ip literal",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL:    "https://example.com/image.png",
					CallbackURL: "http://[::1]/hook",
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accountService := &fakeAccountService{}
			svc := &taskService{
				taskRepo:       newFakeTaskRepo(),
				accountService: accountService,
				logger:         zap.NewNop(),
			}

			resp, err := tt.run(svc)

			if resp != nil {
				t.Fatalf("response = %#v, want nil", resp)
			}
			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if accountService.acquireAvailableCalls != 0 {
				t.Fatalf("AcquireAvailableAccount calls = %d, want 0", accountService.acquireAvailableCalls)
			}
		})
	}
}

func TestCreateTaskTrimsInputBeforePersisting(t *testing.T) {
	tests := []struct {
		name         string
		run          func(*taskService) (*TaskResponse, error)
		wantPrompt   string
		wantCallback string
	}{
		{
			name: "imagine",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateImagineTask(context.Background(), &CreateTaskRequest{
					Prompt:      "  a quiet harbor  ",
					CallbackURL: "  https://callback.example.com/hook  ",
				})
			},
			wantPrompt:   "a quiet harbor",
			wantCallback: "https://callback.example.com/hook",
		},
		{
			name: "describe",
			run: func(svc *taskService) (*TaskResponse, error) {
				return svc.CreateDescribeTask(context.Background(), &CreateDescribeTaskRequest{
					ImageURL:    "  https://example.com/image.png  ",
					CallbackURL: "  https://callback.example.com/hook  ",
				})
			},
			wantPrompt:   "https://example.com/image.png",
			wantCallback: "https://callback.example.com/hook",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskRepo := newFakeTaskRepo()
			taskRepo.createErr = errors.New("stop before enqueue")
			svc := &taskService{
				taskRepo: taskRepo,
				accountService: &fakeAccountService{
					acquireAvailable: &model.Account{ID: 1},
				},
				logger: zap.NewNop(),
			}

			resp, err := tt.run(svc)
			if resp != nil {
				t.Fatalf("response = %#v, want nil after forced create error", resp)
			}
			assertAppErrorCode(t, err, apperrors.ErrCodeTaskCreateFailed)

			task := taskRepo.lastCreated
			if task == nil {
				t.Fatalf("task was not passed to repository")
			}
			if task.Prompt != tt.wantPrompt {
				t.Fatalf("prompt = %q, want %q", task.Prompt, tt.wantPrompt)
			}
			if task.CallbackURL != tt.wantCallback {
				t.Fatalf("callback_url = %q, want %q", task.CallbackURL, tt.wantCallback)
			}
		})
	}
}

func TestGetTaskValidatesAndTrimsTaskID(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	taskRepo.tasks["task-1"] = &model.Task{TaskID: "task-1"}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: &fakeAccountService{},
		logger:         zap.NewNop(),
	}

	task, err := svc.GetTask(context.Background(), "  task-1  ")

	if err != nil {
		t.Fatalf("GetTask returned error: %v", err)
	}
	if task == nil || task.TaskID != "task-1" {
		t.Fatalf("task = %#v, want task-1", task)
	}
	if taskRepo.lastGetByTaskID != "task-1" {
		t.Fatalf("GetByTaskID task_id = %q, want trimmed task-1", taskRepo.lastGetByTaskID)
	}
}

func TestGetTaskRejectsBlankTaskIDBeforeLookup(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: &fakeAccountService{},
		logger:         zap.NewNop(),
	}

	task, err := svc.GetTask(context.Background(), "   ")

	if task != nil {
		t.Fatalf("task = %#v, want nil", task)
	}
	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if taskRepo.getByTaskIDCalls != 0 {
		t.Fatalf("GetByTaskID calls = %d, want 0", taskRepo.getByTaskIDCalls)
	}
}

func TestGetTaskRejectsOverlongTaskIDBeforeLookup(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: &fakeAccountService{},
		logger:         zap.NewNop(),
	}

	task, err := svc.GetTask(context.Background(), strings.Repeat("t", constants.MaxTaskIDLength+1))

	if task != nil {
		t.Fatalf("task = %#v, want nil", task)
	}
	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if taskRepo.getByTaskIDCalls != 0 {
		t.Fatalf("GetByTaskID calls = %d, want 0", taskRepo.getByTaskIDCalls)
	}
}

func TestListTasksValidatesPaginationBeforeRepository(t *testing.T) {
	tests := []struct {
		name   string
		limit  int
		offset int
	}{
		{
			name:   "zero limit",
			limit:  0,
			offset: 0,
		},
		{
			name:   "negative limit",
			limit:  -1,
			offset: 0,
		},
		{
			name:   "negative offset",
			limit:  10,
			offset: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskRepo := newFakeTaskRepo()
			svc := &taskService{
				taskRepo:       taskRepo,
				accountService: &fakeAccountService{},
				logger:         zap.NewNop(),
			}

			tasks, total, err := svc.ListTasks(context.Background(), tt.limit, tt.offset)

			if tasks != nil {
				t.Fatalf("tasks = %#v, want nil", tasks)
			}
			if total != 0 {
				t.Fatalf("total = %d, want 0", total)
			}
			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if taskRepo.listCalls != 0 {
				t.Fatalf("List calls = %d, want 0", taskRepo.listCalls)
			}
		})
	}
}

func TestListTasksClampsMaxLimit(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: &fakeAccountService{},
		logger:         zap.NewNop(),
	}

	_, _, err := svc.ListTasks(context.Background(), constants.MaxListLimit+1, 25)

	if err != nil {
		t.Fatalf("ListTasks returned error: %v", err)
	}
	if taskRepo.listCalls != 1 {
		t.Fatalf("List calls = %d, want 1", taskRepo.listCalls)
	}
	if taskRepo.lastListLimit != constants.MaxListLimit {
		t.Fatalf("limit = %d, want clamped %d", taskRepo.lastListLimit, constants.MaxListLimit)
	}
	if taskRepo.lastListOffset != 25 {
		t.Fatalf("offset = %d, want 25", taskRepo.lastListOffset)
	}
}

func TestPerformTaskActionRejectsInvalidInputBeforeLookup(t *testing.T) {
	tests := []struct {
		name string
		req  *TaskActionRequest
	}{
		{
			name: "nil request",
			req:  nil,
		},
		{
			name: "blank task id",
			req: &TaskActionRequest{
				TaskID:     "   ",
				ActionType: "upscale",
				Index:      1,
			},
		},
		{
			name: "overlong task id",
			req: &TaskActionRequest{
				TaskID:     strings.Repeat("t", constants.MaxTaskIDLength+1),
				ActionType: "upscale",
				Index:      1,
			},
		},
		{
			name: "blank action type",
			req: &TaskActionRequest{
				TaskID:     "parent-task",
				ActionType: "   ",
				Index:      1,
			},
		},
		{
			name: "unsupported action type",
			req: &TaskActionRequest{
				TaskID:     "parent-task",
				ActionType: "reroll",
				Index:      1,
			},
		},
		{
			name: "index below range",
			req: &TaskActionRequest{
				TaskID:     "parent-task",
				ActionType: "upscale",
				Index:      0,
			},
		},
		{
			name: "index above range",
			req: &TaskActionRequest{
				TaskID:     "parent-task",
				ActionType: "upscale",
				Index:      5,
			},
		},
		{
			name: "blank callback",
			req: &TaskActionRequest{
				TaskID:      "parent-task",
				ActionType:  "upscale",
				Index:       1,
				CallbackURL: "   ",
			},
		},
		{
			name: "invalid callback scheme",
			req: &TaskActionRequest{
				TaskID:      "parent-task",
				ActionType:  "upscale",
				Index:       1,
				CallbackURL: "ftp://callback.example.com/hook",
			},
		},
		{
			name: "callback userinfo",
			req: &TaskActionRequest{
				TaskID:      "parent-task",
				ActionType:  "upscale",
				Index:       1,
				CallbackURL: "https://user:pass@callback.example.com/hook",
			},
		},
		{
			name: "callback private ip literal",
			req: &TaskActionRequest{
				TaskID:      "parent-task",
				ActionType:  "upscale",
				Index:       1,
				CallbackURL: "http://192.168.1.8/hook",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskRepo := newFakeTaskRepo()
			accountService := &fakeAccountService{}
			svc := &taskService{
				taskRepo:       taskRepo,
				accountService: accountService,
				logger:         zap.NewNop(),
			}

			resp, err := svc.PerformTaskAction(context.Background(), tt.req)

			if resp != nil {
				t.Fatalf("response = %#v, want nil", resp)
			}
			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if taskRepo.getByTaskIDCalls != 0 {
				t.Fatalf("GetByTaskID calls = %d, want 0", taskRepo.getByTaskIDCalls)
			}
			if accountService.acquireAccountCalls != 0 {
				t.Fatalf("AcquireAccount calls = %d, want 0", accountService.acquireAccountCalls)
			}
		})
	}
}

func TestPerformTaskActionTrimsInputBeforePersisting(t *testing.T) {
	accountID := uint(9)
	buttons := mustMarshalString(t, []string{"MJ::JOB::upsample::1::button-id"})
	taskRepo := newFakeTaskRepo()
	taskRepo.createErr = errors.New("stop before enqueue")
	taskRepo.tasks["parent-task"] = &model.Task{
		TaskID:           "parent-task",
		AccountID:        &accountID,
		Type:             model.TaskTypeImagine,
		Prompt:           "a quiet harbor",
		Status:           model.TaskStatusSuccess,
		DiscordMessageID: "message-1",
		Buttons:          &buttons,
	}
	accountService := &fakeAccountService{
		accounts: map[uint]*model.Account{
			accountID: {
				ID:        accountID,
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				UserToken: "token",
				IsHealthy: true,
			},
		},
	}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		logger:         zap.NewNop(),
	}

	resp, err := svc.PerformTaskAction(context.Background(), &TaskActionRequest{
		TaskID:      "  parent-task  ",
		ActionType:  "  upscale  ",
		Index:       1,
		CallbackURL: "  https://callback.example.com/hook  ",
	})

	if resp != nil {
		t.Fatalf("response = %#v, want nil after forced create error", resp)
	}
	assertAppErrorCode(t, err, apperrors.ErrCodeTaskCreateFailed)
	if taskRepo.lastGetByTaskID != "parent-task" {
		t.Fatalf("GetByTaskID task_id = %q, want trimmed parent-task", taskRepo.lastGetByTaskID)
	}
	if accountService.acquireAccountCalls != 1 {
		t.Fatalf("AcquireAccount calls = %d, want 1", accountService.acquireAccountCalls)
	}

	task := taskRepo.lastCreated
	if task == nil {
		t.Fatalf("task was not passed to repository")
	}
	if task.ParentTaskID != "parent-task" {
		t.Fatalf("parent_task_id = %q, want trimmed parent-task", task.ParentTaskID)
	}
	if task.CallbackURL != "https://callback.example.com/hook" {
		t.Fatalf("callback_url = %q, want trimmed callback", task.CallbackURL)
	}
	if task.Type != model.TaskTypeUpscale {
		t.Fatalf("task type = %q, want %q", task.Type, model.TaskTypeUpscale)
	}
}

func TestPerformTaskActionUnavailableActionDoesNotExposeCustomIDs(t *testing.T) {
	accountID := uint(9)
	buttons := mustMarshalString(t, []string{"MJ::JOB::upsample::1::secret-button-id"})
	taskRepo := seededTaskRepo(&model.Task{
		TaskID:           "parent-task",
		AccountID:        &accountID,
		Type:             model.TaskTypeImagine,
		Prompt:           "a quiet harbor",
		Status:           model.TaskStatusSuccess,
		DiscordMessageID: "message-1",
		Buttons:          &buttons,
	})
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		logger:         zap.NewNop(),
	}

	resp, err := svc.PerformTaskAction(context.Background(), &TaskActionRequest{
		TaskID:     "parent-task",
		ActionType: "zoom_out_2x",
		Index:      1,
	})

	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	for _, forbidden := range []string{"secret-button-id", "MJ::JOB", "custom_id"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error exposed %q: %s", forbidden, err.Error())
		}
	}
	if accountService.acquireAccountCalls != 0 {
		t.Fatalf("AcquireAccount calls = %d, want 0 for unavailable action", accountService.acquireAccountCalls)
	}
}

func TestPerformTaskActionRequiresRequestedButtonIndex(t *testing.T) {
	accountID := uint(9)
	buttons := mustMarshalString(t, []string{
		"MJ::JOB::upsample::1::button-id",
	})
	taskRepo := seededTaskRepo(&model.Task{
		TaskID:           "parent-task",
		AccountID:        &accountID,
		Type:             model.TaskTypeImagine,
		Prompt:           "a quiet harbor",
		Status:           model.TaskStatusSuccess,
		DiscordMessageID: "message-1",
		Buttons:          &buttons,
	})
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		logger:         zap.NewNop(),
	}

	resp, err := svc.PerformTaskAction(context.Background(), &TaskActionRequest{
		TaskID:     "parent-task",
		ActionType: "upscale",
		Index:      4,
	})

	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if accountService.acquireAccountCalls != 0 {
		t.Fatalf("AcquireAccount calls = %d, want 0 when requested index is unavailable", accountService.acquireAccountCalls)
	}
}

func TestPerformTaskActionUnavailableMetadataUsesPublicError(t *testing.T) {
	accountID := uint(9)
	validButtons := mustMarshalString(t, []string{"MJ::JOB::upsample::1::button-id"})
	emptyButtons := ""
	malformedButtons := "not-json"
	emptyButtonArray := "[]"

	tests := []struct {
		name   string
		parent model.Task
	}{
		{
			name: "missing discord message",
			parent: model.Task{
				AccountID: &accountID,
				Buttons:   &validButtons,
			},
		},
		{
			name: "missing buttons",
			parent: model.Task{
				AccountID:        &accountID,
				DiscordMessageID: "message-1",
			},
		},
		{
			name: "empty buttons",
			parent: model.Task{
				AccountID:        &accountID,
				DiscordMessageID: "message-1",
				Buttons:          &emptyButtons,
			},
		},
		{
			name: "malformed buttons",
			parent: model.Task{
				AccountID:        &accountID,
				DiscordMessageID: "message-1",
				Buttons:          &malformedButtons,
			},
		},
		{
			name: "empty button array",
			parent: model.Task{
				AccountID:        &accountID,
				DiscordMessageID: "message-1",
				Buttons:          &emptyButtonArray,
			},
		},
		{
			name: "missing account",
			parent: model.Task{
				DiscordMessageID: "message-1",
				Buttons:          &validButtons,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := tt.parent
			parent.TaskID = "parent-task"
			parent.Type = model.TaskTypeImagine
			parent.Prompt = "a quiet harbor"
			parent.Status = model.TaskStatusSuccess

			taskRepo := seededTaskRepo(&parent)
			accountService := &fakeAccountService{}
			svc := &taskService{
				taskRepo:       taskRepo,
				accountService: accountService,
				logger:         zap.NewNop(),
			}

			resp, err := svc.PerformTaskAction(context.Background(), &TaskActionRequest{
				TaskID:     "parent-task",
				ActionType: "upscale",
				Index:      1,
			})

			if resp != nil {
				t.Fatalf("response = %#v, want nil", resp)
			}
			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if !strings.Contains(err.Error(), "task action is not available for this task") {
				t.Fatalf("error = %q, want public unavailable message", err.Error())
			}
			for _, forbidden := range []string{"discord_message_id", "discord message", "buttons", "account_id", "custom_id", "not-json"} {
				if strings.Contains(strings.ToLower(err.Error()), forbidden) {
					t.Fatalf("error exposed %q: %s", forbidden, err.Error())
				}
			}
			if accountService.acquireAccountCalls != 0 {
				t.Fatalf("AcquireAccount calls = %d, want 0", accountService.acquireAccountCalls)
			}
		})
	}
}

func TestProcessTaskDiscordFailureMarksTaskFailedAndRecordsAccountFailure(t *testing.T) {
	accountID := uint(9)
	taskRepo := newFakeTaskRepo()
	taskRepo.tasks["task-3"] = &model.Task{
		TaskID:    "task-3",
		AccountID: &accountID,
		Status:    model.TaskStatusPending,
	}
	accountService := &fakeAccountService{}
	discordErr := errors.New("discord down")
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        &fakeDiscordAPI{imagineErr: discordErr},
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-3",
		Prompt:    "a quiet harbor",
		AccountID: accountID,
	})

	if !errors.Is(err, discordErr) {
		t.Fatalf("error = %v, want %v", err, discordErr)
	}
	if got := taskRepo.statusUpdates["task-3"]; got != model.TaskStatusProcessing {
		t.Fatalf("first status update = %q, want %q", got, model.TaskStatusProcessing)
	}
	task := taskRepo.tasks["task-3"]
	if task.Status != model.TaskStatusFailed {
		t.Fatalf("task status = %q, want %q", task.Status, model.TaskStatusFailed)
	}
	if task.ErrorMessage != discordErr.Error() {
		t.Fatalf("error message = %q, want %q", task.ErrorMessage, discordErr.Error())
	}
	if task.FinishedAt == nil {
		t.Fatalf("finished_at should be set")
	}
	if !accountService.decremented(accountID) {
		t.Fatalf("expected account %d slot to be released", accountID)
	}
	if len(accountService.recordedResults) != 1 {
		t.Fatalf("recordedResults len = %d, want 1", len(accountService.recordedResults))
	}
	recorded := accountService.recordedResults[0]
	if recorded.accountID != accountID || recorded.success || recorded.lastError != discordErr.Error() {
		t.Fatalf("recorded result = %#v, want account %d failure %q", recorded, accountID, discordErr.Error())
	}
}

func TestProcessTaskDiscordFailureRedactsStoredAndRecordedError(t *testing.T) {
	accountID := uint(9)
	taskRepo := newFakeTaskRepo()
	taskRepo.tasks["task-secret-error"] = &model.Task{
		TaskID:    "task-secret-error",
		AccountID: &accountID,
		Status:    model.TaskStatusPending,
	}
	accountService := &fakeAccountService{}
	discordErr := errors.New(`discord API error: user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`)
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        &fakeDiscordAPI{imagineErr: discordErr},
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-secret-error",
		Prompt:    "a quiet harbor",
		AccountID: accountID,
	})

	if !errors.Is(err, discordErr) {
		t.Fatalf("error = %v, want %v", err, discordErr)
	}

	task := taskRepo.tasks["task-secret-error"]
	if task == nil {
		t.Fatal("task was not updated")
	}
	assertRedactedFailureText(t, task.ErrorMessage)

	if len(accountService.recordedResults) != 1 {
		t.Fatalf("recordedResults len = %d, want 1", len(accountService.recordedResults))
	}
	assertRedactedFailureText(t, accountService.recordedResults[0].lastError)
}

func assertRedactedFailureText(t *testing.T, text string) {
	t.Helper()

	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("failure text exposed %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `user_token="<redacted>"`) || !strings.Contains(text, "https://example.com/hook") {
		t.Fatalf("failure text did not keep useful redacted context: %s", text)
	}
}

func TestProcessTaskUsesLatestAccountConfig(t *testing.T) {
	accountID := uint(9)
	taskRepo := newFakeTaskRepo()
	taskRepo.tasks["task-latest-account"] = &model.Task{
		TaskID:    "task-latest-account",
		AccountID: &accountID,
		Status:    model.TaskStatusPending,
	}
	accountService := &fakeAccountService{
		accounts: map[uint]*model.Account{
			accountID: {
				ID:        accountID,
				GuildID:   "latest-guild",
				ChannelID: "latest-channel",
				UserToken: "latest-token",
				IsHealthy: true,
			},
		},
	}
	discordAPI := &fakeDiscordAPI{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        discordAPI,
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-latest-account",
		Prompt:    "a quiet harbor",
		AccountID: accountID,
	})
	if err != nil {
		t.Fatalf("ProcessTask returned error: %v", err)
	}

	if discordAPI.imagineReq == nil {
		t.Fatalf("discord Imagine was not called")
	}
	if discordAPI.imagineReq.GuildID != "latest-guild" ||
		discordAPI.imagineReq.ChannelID != "latest-channel" ||
		discordAPI.imagineReq.UserToken != "latest-token" {
		t.Fatalf("discord request used stale account config: %#v", discordAPI.imagineReq)
	}
}

func TestProcessTaskSubmissionMarksAccountHealthyWithoutRecordingFinalSuccess(t *testing.T) {
	accountID := uint(9)
	taskRepo := newFakeTaskRepo()
	taskRepo.tasks["task-submit"] = &model.Task{
		TaskID:    "task-submit",
		AccountID: &accountID,
		Status:    model.TaskStatusPending,
	}
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        &fakeDiscordAPI{},
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-submit",
		Prompt:    "a quiet harbor",
		AccountID: accountID,
	})
	if err != nil {
		t.Fatalf("ProcessTask returned error: %v", err)
	}

	if got := taskRepo.statusUpdates["task-submit"]; got != model.TaskStatusSubmitted {
		t.Fatalf("final status update = %q, want %q", got, model.TaskStatusSubmitted)
	}
	if len(accountService.healthyUpdates) != 1 {
		t.Fatalf("healthyUpdates = %#v, want one update", accountService.healthyUpdates)
	}
	healthyUpdate := accountService.healthyUpdates[0]
	if healthyUpdate.accountID != accountID || !healthyUpdate.isHealthy || healthyUpdate.lastError != "" {
		t.Fatalf("healthy update = %#v, want account %d healthy", healthyUpdate, accountID)
	}
	if len(accountService.recordedResults) != 0 {
		t.Fatalf("recordedResults = %#v, want none until terminal success/failure", accountService.recordedResults)
	}
}

func TestProcessTaskProcessingStatusFailureMarksTaskFailedAndReleasesSlot(t *testing.T) {
	accountID := uint(9)
	taskRepo := newFakeTaskRepo()
	taskRepo.updateStatusErr = errors.New("database unavailable")
	taskRepo.tasks["task-processing-fails"] = &model.Task{
		TaskID:    "task-processing-fails",
		AccountID: &accountID,
		Status:    model.TaskStatusPending,
	}
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        &fakeDiscordAPI{},
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-processing-fails",
		Prompt:    "a quiet harbor",
		AccountID: accountID,
	})

	if err == nil {
		t.Fatal("ProcessTask returned nil error, want processing status failure")
	}
	task := taskRepo.tasks["task-processing-fails"]
	if task == nil || task.Status != model.TaskStatusFailed {
		t.Fatalf("task after processing status failure = %#v, want FAILED", task)
	}
	if !accountService.decremented(accountID) {
		t.Fatalf("expected account %d slot to be released", accountID)
	}
	if len(accountService.recordedResults) != 1 {
		t.Fatalf("recordedResults len = %d, want 1", len(accountService.recordedResults))
	}
	if accountService.getAccountByIDCalls != 0 {
		t.Fatalf("GetAccountByID calls = %d, want 0 before Discord call", accountService.getAccountByIDCalls)
	}
}

func TestProcessTaskProcessingStatusFailureDoesNotReleaseWhenTerminalUpdateFails(t *testing.T) {
	accountID := uint(9)
	taskRepo := newFakeTaskRepo()
	taskRepo.updateStatusErr = errors.New("processing status unavailable")
	taskRepo.updateErr = errors.New("terminal status unavailable")
	taskRepo.tasks["task-processing-terminal-fails"] = &model.Task{
		TaskID:    "task-processing-terminal-fails",
		AccountID: &accountID,
		Status:    model.TaskStatusPending,
	}
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        &fakeDiscordAPI{},
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-processing-terminal-fails",
		Prompt:    "a quiet harbor",
		AccountID: accountID,
	})

	if err == nil {
		t.Fatal("ProcessTask returned nil error, want processing status failure")
	}
	task := taskRepo.tasks["task-processing-terminal-fails"]
	if task == nil || task.Status != model.TaskStatusPending {
		t.Fatalf("task after failed terminal update = %#v, want original PENDING task", task)
	}
	if len(accountService.decrementedIDs) != 0 {
		t.Fatalf("decrementedIDs = %#v, want no release before terminal transition", accountService.decrementedIDs)
	}
	if len(accountService.recordedResults) != 0 {
		t.Fatalf("recordedResults = %#v, want none before terminal transition", accountService.recordedResults)
	}
}

func TestProcessQueuedTasksRejectUnidentifiableMessageBeforeRepository(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *taskService) error
	}{
		{
			name: "nil imagine",
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessTask(ctx, nil)
			},
		},
		{
			name: "blank imagine task id",
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessTask(ctx, &TaskMessage{
					TaskID:    "   ",
					Prompt:    "a quiet harbor",
					AccountID: 9,
				})
			},
		},
		{
			name: "overlong imagine task id",
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessTask(ctx, &TaskMessage{
					TaskID:    strings.Repeat("t", constants.MaxTaskIDLength+1),
					Prompt:    "a quiet harbor",
					AccountID: 9,
				})
			},
		},
		{
			name: "nil describe",
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessDescribeTask(ctx, nil)
			},
		},
		{
			name: "blank describe task id",
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessDescribeTask(ctx, &TaskDescribeMessage{
					TaskID:    "   ",
					ImageURL:  "https://example.com/image.png",
					AccountID: 9,
				})
			},
		},
		{
			name: "overlong describe task id",
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessDescribeTask(ctx, &TaskDescribeMessage{
					TaskID:    strings.Repeat("t", constants.MaxTaskIDLength+1),
					ImageURL:  "https://example.com/image.png",
					AccountID: 9,
				})
			},
		},
		{
			name: "nil action",
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessActionTask(ctx, nil)
			},
		},
		{
			name: "blank action task id",
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessActionTask(ctx, &TaskActionMessage{
					TaskID:           "   ",
					CustomID:         "custom-1",
					DiscordMessageID: "message-1",
					AccountID:        9,
				})
			},
		},
		{
			name: "overlong action task id",
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessActionTask(ctx, &TaskActionMessage{
					TaskID:           strings.Repeat("t", constants.MaxTaskIDLength+1),
					CustomID:         "custom-1",
					DiscordMessageID: "message-1",
					AccountID:        9,
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskRepo := newFakeTaskRepo()
			accountService := &fakeAccountService{}
			svc := &taskService{
				taskRepo:       taskRepo,
				accountService: accountService,
				discord:        &fakeDiscordAPI{},
				taskConfig:     &config.TaskConfig{MaxRetries: 0},
				logger:         zap.NewNop(),
			}

			err := tt.run(context.Background(), svc)

			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			if len(taskRepo.statusUpdates) != 0 {
				t.Fatalf("statusUpdates = %#v, want none", taskRepo.statusUpdates)
			}
			if accountService.getAccountByIDCalls != 0 {
				t.Fatalf("GetAccountByID calls = %d, want 0", accountService.getAccountByIDCalls)
			}
			if len(accountService.decrementedIDs) != 0 {
				t.Fatalf("decrementedIDs = %#v, want none", accountService.decrementedIDs)
			}
			if len(accountService.recordedResults) != 0 {
				t.Fatalf("recordedResults = %#v, want none", accountService.recordedResults)
			}
		})
	}
}

func TestProcessQueuedTasksFailIdentifiableInvalidMessages(t *testing.T) {
	tests := []struct {
		name            string
		taskID          string
		accountID       uint
		run             func(context.Context, *taskService) error
		wantSlotRelease bool
	}{
		{
			name:      "blank imagine prompt",
			taskID:    "task-imagine-invalid",
			accountID: 9,
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessTask(ctx, &TaskMessage{
					TaskID:    "task-imagine-invalid",
					Prompt:    "   ",
					AccountID: 9,
				})
			},
			wantSlotRelease: true,
		},
		{
			name:      "blank describe image",
			taskID:    "task-describe-invalid",
			accountID: 9,
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessDescribeTask(ctx, &TaskDescribeMessage{
					TaskID:    "task-describe-invalid",
					ImageURL:  "   ",
					AccountID: 9,
				})
			},
			wantSlotRelease: true,
		},
		{
			name:      "invalid describe image URL",
			taskID:    "task-describe-url-invalid",
			accountID: 9,
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessDescribeTask(ctx, &TaskDescribeMessage{
					TaskID:    "task-describe-url-invalid",
					ImageURL:  "ftp://example.com/image.png",
					AccountID: 9,
				})
			},
			wantSlotRelease: true,
		},
		{
			name:      "describe image URL userinfo",
			taskID:    "task-describe-url-userinfo",
			accountID: 9,
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessDescribeTask(ctx, &TaskDescribeMessage{
					TaskID:    "task-describe-url-userinfo",
					ImageURL:  "https://user:pass@example.com/image.png",
					AccountID: 9,
				})
			},
			wantSlotRelease: true,
		},
		{
			name:      "blank action discord message id",
			taskID:    "task-action-message-invalid",
			accountID: 9,
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessActionTask(ctx, &TaskActionMessage{
					TaskID:           "task-action-message-invalid",
					CustomID:         "custom-1",
					DiscordMessageID: "   ",
					AccountID:        9,
				})
			},
			wantSlotRelease: true,
		},
		{
			name:      "overlong action discord message id",
			taskID:    "task-action-message-overlong",
			accountID: 9,
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessActionTask(ctx, &TaskActionMessage{
					TaskID:           "task-action-message-overlong",
					CustomID:         "custom-1",
					DiscordMessageID: strings.Repeat("m", constants.MaxDiscordMessageIDLength+1),
					AccountID:        9,
				})
			},
			wantSlotRelease: true,
		},
		{
			name:      "blank action custom id",
			taskID:    "task-action-custom-invalid",
			accountID: 9,
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessActionTask(ctx, &TaskActionMessage{
					TaskID:           "task-action-custom-invalid",
					CustomID:         "   ",
					DiscordMessageID: "message-1",
					AccountID:        9,
				})
			},
			wantSlotRelease: true,
		},
		{
			name:      "missing account id",
			taskID:    "task-missing-account-invalid",
			accountID: 0,
			run: func(ctx context.Context, svc *taskService) error {
				return svc.ProcessTask(ctx, &TaskMessage{
					TaskID: "task-missing-account-invalid",
					Prompt: "a quiet harbor",
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskRepo := newFakeTaskRepo()
			taskRepo.tasks[tt.taskID] = &model.Task{
				TaskID: tt.taskID,
				Status: model.TaskStatusPending,
			}
			accountService := &fakeAccountService{}
			svc := &taskService{
				taskRepo:       taskRepo,
				accountService: accountService,
				discord:        &fakeDiscordAPI{},
				taskConfig:     &config.TaskConfig{MaxRetries: 0},
				logger:         zap.NewNop(),
			}

			err := tt.run(context.Background(), svc)

			assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
			task := taskRepo.tasks[tt.taskID]
			if task == nil {
				t.Fatalf("task %s was not updated", tt.taskID)
			}
			if task.Status != model.TaskStatusFailed {
				t.Fatalf("task status = %q, want FAILED", task.Status)
			}
			if accountService.getAccountByIDCalls != 0 {
				t.Fatalf("GetAccountByID calls = %d, want 0", accountService.getAccountByIDCalls)
			}
			if tt.wantSlotRelease && !accountService.decremented(tt.accountID) {
				t.Fatalf("expected account %d slot to be released", tt.accountID)
			}
			if !tt.wantSlotRelease && len(accountService.decrementedIDs) != 0 {
				t.Fatalf("decrementedIDs = %#v, want none", accountService.decrementedIDs)
			}
			if tt.wantSlotRelease && len(accountService.recordedResults) != 1 {
				t.Fatalf("recordedResults len = %d, want 1", len(accountService.recordedResults))
			}
			if !tt.wantSlotRelease && len(accountService.recordedResults) != 0 {
				t.Fatalf("recordedResults = %#v, want none", accountService.recordedResults)
			}
		})
	}
}

func TestProcessTaskUnavailableAccountFailsTaskAndReleasesSlot(t *testing.T) {
	accountID := uint(11)
	tests := []struct {
		name    string
		account *model.Account
	}{
		{
			name: "disabled account",
			account: &model.Account{
				ID:         accountID,
				GuildID:    "guild-1",
				ChannelID:  "channel-1",
				UserToken:  "token",
				IsDisabled: true,
				IsHealthy:  true,
			},
		},
		{
			name: "unhealthy account",
			account: &model.Account{
				ID:        accountID,
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				UserToken: "token",
				IsHealthy: false,
			},
		},
		{
			name: "missing token",
			account: &model.Account{
				ID:        accountID,
				GuildID:   "guild-1",
				ChannelID: "channel-1",
				IsHealthy: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskRepo := newFakeTaskRepo()
			taskRepo.tasks["task-account-unavailable"] = &model.Task{
				TaskID:    "task-account-unavailable",
				AccountID: &accountID,
				Status:    model.TaskStatusPending,
			}
			accountService := &fakeAccountService{
				accounts: map[uint]*model.Account{accountID: tt.account},
			}
			discordAPI := &fakeDiscordAPI{}
			svc := &taskService{
				taskRepo:       taskRepo,
				accountService: accountService,
				discord:        discordAPI,
				taskConfig:     &config.TaskConfig{MaxRetries: 0},
				logger:         zap.NewNop(),
			}

			err := svc.ProcessTask(context.Background(), &TaskMessage{
				TaskID:    "task-account-unavailable",
				Prompt:    "a quiet harbor",
				AccountID: accountID,
			})

			assertAppErrorCode(t, err, apperrors.ErrCodeAccountUnavailable)
			if discordAPI.imagineReq != nil {
				t.Fatalf("discord should not be called for unavailable account")
			}
			if !accountService.decremented(accountID) {
				t.Fatalf("expected account slot to be released")
			}

			task := taskRepo.tasks["task-account-unavailable"]
			if task.Status != model.TaskStatusFailed {
				t.Fatalf("task status = %q, want FAILED", task.Status)
			}
		})
	}
}

func TestProcessTaskRejectsQueueAccountMismatchAndReleasesStoredAccount(t *testing.T) {
	storedAccountID := uint(12)
	queuedAccountID := uint(99)
	taskRepo := newFakeTaskRepo()
	taskRepo.tasks["task-account-mismatch"] = &model.Task{
		TaskID:    "task-account-mismatch",
		AccountID: &storedAccountID,
		Status:    model.TaskStatusPending,
	}
	accountService := &fakeAccountService{
		accounts: map[uint]*model.Account{
			storedAccountID: {
				ID:        storedAccountID,
				GuildID:   "stored-guild",
				ChannelID: "stored-channel",
				UserToken: "stored-token",
				IsHealthy: true,
			},
			queuedAccountID: {
				ID:        queuedAccountID,
				GuildID:   "queued-guild",
				ChannelID: "queued-channel",
				UserToken: "queued-token",
				IsHealthy: true,
			},
		},
	}
	discordAPI := &fakeDiscordAPI{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        discordAPI,
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-account-mismatch",
		Prompt:    "a quiet harbor",
		AccountID: queuedAccountID,
	})

	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if discordAPI.imagineReq != nil {
		t.Fatalf("discord should not be called when queue account mismatches stored task account")
	}
	if accountService.getAccountByIDCalls != 0 {
		t.Fatalf("GetAccountByID calls = %d, want 0 before account mismatch is resolved", accountService.getAccountByIDCalls)
	}
	if !accountService.decremented(storedAccountID) {
		t.Fatalf("expected stored account %d slot to be released", storedAccountID)
	}
	if accountService.decremented(queuedAccountID) {
		t.Fatalf("queued account %d slot should not be released", queuedAccountID)
	}
	if len(accountService.recordedResults) != 1 || accountService.recordedResults[0].accountID != storedAccountID {
		t.Fatalf("recordedResults = %#v, want one failure for stored account", accountService.recordedResults)
	}
	if task := taskRepo.tasks["task-account-mismatch"]; task.Status != model.TaskStatusFailed {
		t.Fatalf("task status = %q, want FAILED", task.Status)
	}
}

func TestProcessTaskMissingAccountIDUsesStoredTaskAccountForCleanup(t *testing.T) {
	accountID := uint(12)
	taskRepo := newFakeTaskRepo()
	taskRepo.tasks["task-missing-account"] = &model.Task{
		TaskID:    "task-missing-account",
		Status:    model.TaskStatusPending,
		AccountID: &accountID,
	}
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        &fakeDiscordAPI{},
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID: "task-missing-account",
		Prompt: "a quiet harbor",
	})

	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if !accountService.decremented(accountID) {
		t.Fatalf("expected stored account slot to be released")
	}
	if len(accountService.recordedResults) != 1 || accountService.recordedResults[0].accountID != accountID {
		t.Fatalf("recordedResults = %#v, want one failure for stored account", accountService.recordedResults)
	}
	if task := taskRepo.tasks["task-missing-account"]; task.Status != model.TaskStatusFailed {
		t.Fatalf("task status = %q, want FAILED", task.Status)
	}
}

func TestProcessTaskMissingAccountIDWithoutStoredAccountDoesNotTouchAccountCounters(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	taskRepo.tasks["task-missing-account"] = &model.Task{
		TaskID: "task-missing-account",
		Status: model.TaskStatusPending,
	}
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        &fakeDiscordAPI{},
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID: "task-missing-account",
		Prompt: "a quiet harbor",
	})

	assertAppErrorCode(t, err, apperrors.ErrCodeInvalidInput)
	if len(accountService.decrementedIDs) != 0 {
		t.Fatalf("decrementedIDs = %#v, want none", accountService.decrementedIDs)
	}
	if len(accountService.recordedResults) != 0 {
		t.Fatalf("recordedResults = %#v, want none", accountService.recordedResults)
	}
	if task := taskRepo.tasks["task-missing-account"]; task.Status != model.TaskStatusFailed {
		t.Fatalf("task status = %q, want FAILED", task.Status)
	}
}

func TestRejectQueueMessageFailsTaskAndReleasesStoredAccount(t *testing.T) {
	accountID := uint(14)
	queuedAccountID := uint(99)
	taskRepo := newFakeTaskRepo()
	taskRepo.tasks["task-reject"] = &model.Task{
		TaskID:    "task-reject",
		Status:    model.TaskStatusPending,
		AccountID: &accountID,
	}
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.RejectQueueMessage(" task-reject ", queuedAccountID, errors.New("missing kind"))

	if err != nil {
		t.Fatalf("RejectQueueMessage returned error: %v", err)
	}
	if task := taskRepo.tasks["task-reject"]; task.Status != model.TaskStatusFailed {
		t.Fatalf("task status = %q, want FAILED", task.Status)
	}
	if !accountService.decremented(accountID) {
		t.Fatalf("expected stored account slot to be released")
	}
	if accountService.decremented(queuedAccountID) {
		t.Fatalf("queued account %d slot should not be released", queuedAccountID)
	}
	if len(accountService.recordedResults) != 1 || accountService.recordedResults[0].accountID != accountID {
		t.Fatalf("recordedResults = %#v, want one failure for stored account", accountService.recordedResults)
	}
}

func TestProcessTaskCanceledRetryStillCleansUpTaskAndAccount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	accountID := uint(10)
	taskRepo := newFakeTaskRepo()
	taskRepo.rejectCanceledContext = true
	taskRepo.tasks["task-4"] = &model.Task{
		TaskID:    "task-4",
		AccountID: &accountID,
		Status:    model.TaskStatusPending,
	}
	accountService := &fakeAccountService{rejectCanceledContext: true}
	discordErr := errors.New("discord down")
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord: &fakeDiscordAPI{
			imagineErr: discordErr,
			imagineHook: func() {
				cancel()
			},
		},
		taskConfig: &config.TaskConfig{MaxRetries: 1},
		logger:     zap.NewNop(),
	}

	err := svc.ProcessTask(ctx, &TaskMessage{
		TaskID:    "task-4",
		Prompt:    "a quiet harbor",
		AccountID: accountID,
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	task := taskRepo.tasks["task-4"]
	if task.Status != model.TaskStatusFailed {
		t.Fatalf("task status = %q, want %q", task.Status, model.TaskStatusFailed)
	}
	if !accountService.decremented(accountID) {
		t.Fatalf("expected account %d slot to be released", accountID)
	}
	if len(accountService.recordedResults) != 1 {
		t.Fatalf("recordedResults len = %d, want 1", len(accountService.recordedResults))
	}
}

func TestProcessTaskAlreadyTerminalDoesNotReleaseAccountSlot(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	taskRepo.updateStatusErr = apperrors.NewTaskAlreadyTerminal("task-terminal", string(model.TaskStatusTimeout))
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        &fakeDiscordAPI{},
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-terminal",
		Prompt:    "a quiet harbor",
		AccountID: 10,
	})

	assertAppErrorCode(t, err, apperrors.ErrCodeTaskAlreadyTerminal)
	if len(accountService.decrementedIDs) != 0 {
		t.Fatalf("decrementedIDs = %#v, want no release for terminal task", accountService.decrementedIDs)
	}
	if len(accountService.recordedResults) != 0 {
		t.Fatalf("recordedResults = %#v, want none", accountService.recordedResults)
	}
}

func TestProcessTaskFailureDoesNotReleaseSlotWhenTerminalUpdateSkipped(t *testing.T) {
	accountID := uint(10)
	taskRepo := newFakeTaskRepo()
	taskRepo.terminalTransitioned = false
	taskRepo.tasks["task-terminal-race"] = &model.Task{
		TaskID:    "task-terminal-race",
		AccountID: &accountID,
		Status:    model.TaskStatusTimeout,
	}
	accountService := &fakeAccountService{}
	discordErr := errors.New("discord down")
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		discord:        &fakeDiscordAPI{imagineErr: discordErr},
		taskConfig:     &config.TaskConfig{MaxRetries: 0},
		logger:         zap.NewNop(),
	}

	err := svc.ProcessTask(context.Background(), &TaskMessage{
		TaskID:    "task-terminal-race",
		Prompt:    "a quiet harbor",
		AccountID: accountID,
	})

	if !errors.Is(err, discordErr) {
		t.Fatalf("error = %v, want %v", err, discordErr)
	}
	if len(accountService.decrementedIDs) != 0 {
		t.Fatalf("decrementedIDs = %#v, want no release when terminal update is skipped", accountService.decrementedIDs)
	}
	if len(accountService.recordedResults) != 0 {
		t.Fatalf("recordedResults = %#v, want none when terminal update is skipped", accountService.recordedResults)
	}
}

func TestWithRetryStopsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := &taskService{
		taskConfig: &config.TaskConfig{MaxRetries: 3},
		logger:     zap.NewNop(),
	}

	attempts := 0
	discordErr := errors.New("discord down")
	err := svc.withRetry(ctx, "task-4", func() error {
		attempts++
		cancel()
		return discordErr
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestSweepTimedOutTasksMarksTerminalAndReleasesAccountSlots(t *testing.T) {
	accountID := uint(12)
	taskRepo := newFakeTaskRepo()
	taskRepo.staleTasks = []*model.Task{
		{
			TaskID:    "task-timeout-1",
			Status:    model.TaskStatusProcessing,
			AccountID: &accountID,
		},
		{
			TaskID: "task-timeout-2",
			Status: model.TaskStatusSubmitted,
		},
		nil,
	}
	accountService := &fakeAccountService{}
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: accountService,
		logger:         zap.NewNop(),
	}

	count, err := svc.SweepTimedOutTasks(context.Background(), time.Now(), 0)
	if err != nil {
		t.Fatalf("SweepTimedOutTasks returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}

	for _, taskID := range []string{"task-timeout-1", "task-timeout-2"} {
		task := taskRepo.tasks[taskID]
		if task == nil {
			t.Fatalf("task %s was not updated", taskID)
		}
		if task.Status != model.TaskStatusTimeout {
			t.Fatalf("task %s status = %q, want TIMEOUT", taskID, task.Status)
		}
		if task.ErrorMessage != taskTimedOutMessage {
			t.Fatalf("task %s error_message = %q, want timeout message", taskID, task.ErrorMessage)
		}
		if task.FinishedAt == nil {
			t.Fatalf("task %s finished_at should be set", taskID)
		}
	}

	if !accountService.decremented(accountID) {
		t.Fatalf("expected account %d slot to be released", accountID)
	}
	if len(accountService.decrementedIDs) != 1 {
		t.Fatalf("decrementedIDs = %#v, want only account %d", accountService.decrementedIDs, accountID)
	}
	if len(accountService.recordedResults) != 1 {
		t.Fatalf("recordedResults len = %d, want 1", len(accountService.recordedResults))
	}
	recorded := accountService.recordedResults[0]
	if recorded.accountID != accountID || recorded.success || recorded.lastError != taskTimedOutMessage {
		t.Fatalf("recorded result = %#v, want timeout failure for account %d", recorded, accountID)
	}
}

func TestPerformTaskActionPreservesParentLookupAppError(t *testing.T) {
	taskRepo := newFakeTaskRepo()
	taskRepo.getByTaskIDErr = apperrors.NewDatabaseError(errors.New("database unavailable"))
	svc := &taskService{
		taskRepo:       taskRepo,
		accountService: &fakeAccountService{},
		logger:         zap.NewNop(),
	}

	resp, err := svc.PerformTaskAction(context.Background(), &TaskActionRequest{
		TaskID:     "parent-task",
		ActionType: "upscale",
		Index:      1,
	})

	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	assertAppErrorCode(t, err, apperrors.ErrCodeDatabaseError)
}

func mustMarshalString(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	return string(data)
}

type fakeTaskRepo struct {
	tasks                 map[string]*model.Task
	createCount           int
	createErr             error
	lastCreated           *model.Task
	getByTaskIDErr        error
	getByTaskIDCalls      int
	lastGetByTaskID       string
	updateErr             error
	updateStatusErr       error
	rejectCanceledContext bool
	statusUpdates         map[string]model.TaskStatus
	staleTasks            []*model.Task
	terminalTransitioned  bool
	listCalls             int
	lastListLimit         int
	lastListOffset        int
}

func newFakeTaskRepo() *fakeTaskRepo {
	return &fakeTaskRepo{
		tasks:                make(map[string]*model.Task),
		statusUpdates:        make(map[string]model.TaskStatus),
		terminalTransitioned: true,
	}
}

func seededTaskRepo(tasks ...*model.Task) *fakeTaskRepo {
	repo := newFakeTaskRepo()
	for _, task := range tasks {
		if task != nil {
			repo.tasks[task.TaskID] = task
		}
	}
	return repo
}

func (r *fakeTaskRepo) Create(ctx context.Context, task *model.Task) error {
	if err := r.contextErr(ctx); err != nil {
		return err
	}
	r.createCount++
	r.lastCreated = task
	if r.createErr != nil {
		return r.createErr
	}
	r.tasks[task.TaskID] = task
	return nil
}

func (r *fakeTaskRepo) GetByTaskID(ctx context.Context, taskID string) (*model.Task, error) {
	if err := r.contextErr(ctx); err != nil {
		return nil, err
	}
	r.getByTaskIDCalls++
	r.lastGetByTaskID = taskID
	if r.getByTaskIDErr != nil {
		return nil, r.getByTaskIDErr
	}
	return r.tasks[taskID], nil
}

func (r *fakeTaskRepo) Update(ctx context.Context, task *model.Task) error {
	if err := r.contextErr(ctx); err != nil {
		return err
	}
	if r.updateErr != nil {
		return r.updateErr
	}
	r.tasks[task.TaskID] = task
	return nil
}

func (r *fakeTaskRepo) UpdateTerminal(ctx context.Context, task *model.Task) (bool, error) {
	if err := r.contextErr(ctx); err != nil {
		return false, err
	}
	if r.updateErr != nil {
		return false, r.updateErr
	}
	if !r.terminalTransitioned {
		return false, nil
	}
	r.tasks[task.TaskID] = task
	return true, nil
}

func (r *fakeTaskRepo) UpdateStatus(ctx context.Context, taskID string, status model.TaskStatus) error {
	if err := r.contextErr(ctx); err != nil {
		return err
	}
	if r.updateStatusErr != nil {
		return r.updateStatusErr
	}
	r.statusUpdates[taskID] = status
	if task := r.tasks[taskID]; task != nil {
		task.Status = status
	}
	return nil
}

func (r *fakeTaskRepo) UpdateOSSImageURL(ctx context.Context, taskID string, ossURL string) error {
	if err := r.contextErr(ctx); err != nil {
		return err
	}
	if task := r.tasks[taskID]; task != nil {
		task.OSSImageURL = ossURL
	}
	return nil
}

func (r *fakeTaskRepo) List(ctx context.Context, limit, offset int) ([]model.Task, int64, error) {
	if err := r.contextErr(ctx); err != nil {
		return nil, 0, err
	}
	r.listCalls++
	r.lastListLimit = limit
	r.lastListOffset = offset
	return nil, 0, nil
}

func (r *fakeTaskRepo) GetByDiscordMessageID(ctx context.Context, messageID string) (*model.Task, error) {
	if err := r.contextErr(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *fakeTaskRepo) GetDiscordActiveTasks(ctx context.Context, limit int) ([]*model.Task, error) {
	if err := r.contextErr(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *fakeTaskRepo) GetStaleActiveTasks(ctx context.Context, cutoff time.Time, limit int) ([]*model.Task, error) {
	if err := r.contextErr(ctx); err != nil {
		return nil, err
	}
	return r.staleTasks, nil
}

func (r *fakeTaskRepo) contextErr(ctx context.Context) error {
	if r.rejectCanceledContext {
		return ctx.Err()
	}
	return nil
}

type fakeAccountService struct {
	decrementedIDs        []uint
	recordedResults       []recordedTaskResult
	healthyUpdates        []accountHealthUpdate
	rejectCanceledContext bool
	accounts              map[uint]*model.Account
	getAccountByIDCalls   int
	lastGetAccountID      uint
	acquireAvailableErr   error
	acquireAvailable      *model.Account
	acquireAvailableCalls int
	acquireAccountErr     error
	acquireAccountCalls   int
	lastAcquireAccountID  uint
}

type recordedTaskResult struct {
	accountID uint
	success   bool
	lastError string
}

type accountHealthUpdate struct {
	accountID uint
	isHealthy bool
	lastError string
}

func (s *fakeAccountService) CreateAccount(ctx context.Context, req *CreateAccountRequest) (*model.Account, error) {
	return nil, nil
}

func (s *fakeAccountService) GetAccountByID(ctx context.Context, id uint) (*model.Account, error) {
	s.getAccountByIDCalls++
	s.lastGetAccountID = id
	if s.rejectCanceledContext {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if s.accounts != nil {
		if account := s.accounts[id]; account != nil {
			copy := *account
			return &copy, nil
		}
	}
	return &model.Account{
		ID:        id,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token",
		IsHealthy: true,
	}, nil
}

func (s *fakeAccountService) ListAccounts(ctx context.Context) ([]model.Account, error) {
	return nil, nil
}

func (s *fakeAccountService) UpdateAccount(ctx context.Context, id uint, req *UpdateAccountRequest) (*model.Account, error) {
	return nil, nil
}

func (s *fakeAccountService) DeleteAccount(ctx context.Context, id uint) error {
	return nil
}

func (s *fakeAccountService) AcquireAvailableAccount(ctx context.Context) (*model.Account, error) {
	s.acquireAvailableCalls++
	if s.acquireAvailableErr != nil {
		return nil, s.acquireAvailableErr
	}
	return s.acquireAvailable, nil
}

func (s *fakeAccountService) AcquireAccount(ctx context.Context, id uint) (*model.Account, error) {
	s.acquireAccountCalls++
	s.lastAcquireAccountID = id
	if s.acquireAccountErr != nil {
		return nil, s.acquireAccountErr
	}
	if s.accounts != nil {
		if account := s.accounts[id]; account != nil {
			copy := *account
			return &copy, nil
		}
	}
	return &model.Account{
		ID:        id,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token",
		IsHealthy: true,
	}, nil
}

func (s *fakeAccountService) SetAccountHealthy(ctx context.Context, id uint, isHealthy bool, lastError string) error {
	s.healthyUpdates = append(s.healthyUpdates, accountHealthUpdate{
		accountID: id,
		isHealthy: isHealthy,
		lastError: lastError,
	})
	return nil
}

func (s *fakeAccountService) DecrementJobs(ctx context.Context, id uint) error {
	if s.rejectCanceledContext {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.decrementedIDs = append(s.decrementedIDs, id)
	return nil
}

func (s *fakeAccountService) RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error {
	if s.rejectCanceledContext {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.recordedResults = append(s.recordedResults, recordedTaskResult{
		accountID: id,
		success:   success,
		lastError: lastError,
	})
	return nil
}

func (s *fakeAccountService) decremented(id uint) bool {
	for _, got := range s.decrementedIDs {
		if got == id {
			return true
		}
	}
	return false
}

type fakeDiscordAPI struct {
	imagineErr  error
	describeErr error
	actionErr   error
	imagineHook func()
	imagineReq  *discord.ImagineRequest
	describeReq *discord.DescribeRequest
	actionReq   *discord.ButtonActionRequest
}

func (d *fakeDiscordAPI) Imagine(ctx context.Context, req *discord.ImagineRequest) error {
	if d.imagineHook != nil {
		d.imagineHook()
	}
	if req != nil {
		copy := *req
		d.imagineReq = &copy
	}
	return d.imagineErr
}

func (d *fakeDiscordAPI) Describe(ctx context.Context, req *discord.DescribeRequest) error {
	if req != nil {
		copy := *req
		d.describeReq = &copy
	}
	return d.describeErr
}

func (d *fakeDiscordAPI) PerformButtonAction(ctx context.Context, req *discord.ButtonActionRequest) error {
	if req != nil {
		copy := *req
		d.actionReq = &copy
	}
	return d.actionErr
}
