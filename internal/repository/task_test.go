package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/pkg/constants"
	apperrors "github.com/trae/midjourney-api/pkg/errors"
	"gorm.io/gorm"
)

func TestTaskRepositoryRejectsInvalidInputBeforeDatabase(t *testing.T) {
	repo := &taskRepository{}
	ctx := context.Background()

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "create nil task",
			run: func() error {
				return repo.Create(ctx, nil)
			},
		},
		{
			name: "create blank task id",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID: "   ",
					Status: model.TaskStatusPending,
				})
			},
		},
		{
			name: "create overlong task id",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID: strings.Repeat("t", constants.MaxTaskIDLength+1),
					Type:   model.TaskTypeImagine,
					Status: model.TaskStatusPending,
				})
			},
		},
		{
			name: "create overlong parent task id",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID:       "task-1",
					ParentTaskID: strings.Repeat("p", constants.MaxTaskIDLength+1),
					Type:         model.TaskTypeImagine,
					Status:       model.TaskStatusPending,
				})
			},
		},
		{
			name: "create overlong discord message id",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID:           "task-1",
					Type:             model.TaskTypeImagine,
					Status:           model.TaskStatusPending,
					DiscordMessageID: strings.Repeat("m", constants.MaxDiscordMessageIDLength+1),
				})
			},
		},
		{
			name: "create blank status",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID: "task-1",
					Type:   model.TaskTypeImagine,
				})
			},
		},
		{
			name: "create unknown status",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID: "task-1",
					Type:   model.TaskTypeImagine,
					Status: model.TaskStatus("BROKEN"),
				})
			},
		},
		{
			name: "create blank type",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID: "task-1",
					Status: model.TaskStatusPending,
				})
			},
		},
		{
			name: "create unknown type",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID: "task-1",
					Type:   model.TaskType("BROKEN"),
					Status: model.TaskStatusPending,
				})
			},
		},
		{
			name: "create negative progress",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID:   "task-1",
					Type:     model.TaskTypeImagine,
					Status:   model.TaskStatusPending,
					Progress: -1,
				})
			},
		},
		{
			name: "create progress above maximum",
			run: func() error {
				return repo.Create(ctx, &model.Task{
					TaskID:   "task-1",
					Type:     model.TaskTypeImagine,
					Status:   model.TaskStatusPending,
					Progress: 101,
				})
			},
		},
		{
			name: "get blank task id",
			run: func() error {
				_, err := repo.GetByTaskID(ctx, "   ")
				return err
			},
		},
		{
			name: "get overlong task id",
			run: func() error {
				_, err := repo.GetByTaskID(ctx, strings.Repeat("t", constants.MaxTaskIDLength+1))
				return err
			},
		},
		{
			name: "get by blank discord message id",
			run: func() error {
				_, err := repo.GetByDiscordMessageID(ctx, "   ")
				return err
			},
		},
		{
			name: "get by overlong discord message id",
			run: func() error {
				_, err := repo.GetByDiscordMessageID(ctx, strings.Repeat("m", constants.MaxDiscordMessageIDLength+1))
				return err
			},
		},
		{
			name: "update nil task",
			run: func() error {
				return repo.Update(ctx, nil)
			},
		},
		{
			name: "update blank task id",
			run: func() error {
				return repo.Update(ctx, &model.Task{
					TaskID: "   ",
					Status: model.TaskStatusPending,
				})
			},
		},
		{
			name: "update overlong task id",
			run: func() error {
				return repo.Update(ctx, &model.Task{
					TaskID: strings.Repeat("t", constants.MaxTaskIDLength+1),
					Status: model.TaskStatusPending,
				})
			},
		},
		{
			name: "update overlong discord message id",
			run: func() error {
				return repo.Update(ctx, &model.Task{
					TaskID:           "task-1",
					Status:           model.TaskStatusPending,
					DiscordMessageID: strings.Repeat("m", constants.MaxDiscordMessageIDLength+1),
				})
			},
		},
		{
			name: "update unknown status",
			run: func() error {
				return repo.Update(ctx, &model.Task{
					TaskID: "task-1",
					Status: model.TaskStatus("BROKEN"),
				})
			},
		},
		{
			name: "update negative progress",
			run: func() error {
				return repo.Update(ctx, &model.Task{
					TaskID:   "task-1",
					Status:   model.TaskStatusPending,
					Progress: -1,
				})
			},
		},
		{
			name: "update progress above maximum",
			run: func() error {
				return repo.Update(ctx, &model.Task{
					TaskID:   "task-1",
					Status:   model.TaskStatusPending,
					Progress: 101,
				})
			},
		},
		{
			name: "update terminal status",
			run: func() error {
				return repo.Update(ctx, &model.Task{
					TaskID: "task-1",
					Type:   model.TaskTypeImagine,
					Status: model.TaskStatusSuccess,
				})
			},
		},
		{
			name: "update terminal nil task",
			run: func() error {
				_, err := repo.UpdateTerminal(ctx, nil)
				return err
			},
		},
		{
			name: "update terminal non terminal status",
			run: func() error {
				_, err := repo.UpdateTerminal(ctx, &model.Task{
					TaskID: "task-1",
					Status: model.TaskStatusProcessing,
				})
				return err
			},
		},
		{
			name: "update terminal unknown status",
			run: func() error {
				_, err := repo.UpdateTerminal(ctx, &model.Task{
					TaskID: "task-1",
					Status: model.TaskStatus("BROKEN"),
				})
				return err
			},
		},
		{
			name: "update terminal overlong discord message id",
			run: func() error {
				_, err := repo.UpdateTerminal(ctx, &model.Task{
					TaskID:           "task-1",
					Status:           model.TaskStatusFailed,
					DiscordMessageID: strings.Repeat("m", constants.MaxDiscordMessageIDLength+1),
				})
				return err
			},
		},
		{
			name: "update terminal negative progress",
			run: func() error {
				_, err := repo.UpdateTerminal(ctx, &model.Task{
					TaskID:   "task-1",
					Status:   model.TaskStatusFailed,
					Progress: -1,
				})
				return err
			},
		},
		{
			name: "update terminal progress above maximum",
			run: func() error {
				_, err := repo.UpdateTerminal(ctx, &model.Task{
					TaskID:   "task-1",
					Status:   model.TaskStatusFailed,
					Progress: 101,
				})
				return err
			},
		},
		{
			name: "update status blank task id",
			run: func() error {
				return repo.UpdateStatus(ctx, "   ", model.TaskStatusProcessing)
			},
		},
		{
			name: "update status overlong task id",
			run: func() error {
				return repo.UpdateStatus(ctx, strings.Repeat("t", constants.MaxTaskIDLength+1), model.TaskStatusProcessing)
			},
		},
		{
			name: "update status blank status",
			run: func() error {
				return repo.UpdateStatus(ctx, "task-1", "")
			},
		},
		{
			name: "update status terminal status",
			run: func() error {
				return repo.UpdateStatus(ctx, "task-1", model.TaskStatusFailed)
			},
		},
		{
			name: "update status unknown status",
			run: func() error {
				return repo.UpdateStatus(ctx, "task-1", model.TaskStatus("BROKEN"))
			},
		},
		{
			name: "update oss blank task id",
			run: func() error {
				return repo.UpdateOSSImageURL(ctx, "   ", "https://oss.example.com/image.png")
			},
		},
		{
			name: "update oss overlong task id",
			run: func() error {
				return repo.UpdateOSSImageURL(ctx, strings.Repeat("t", constants.MaxTaskIDLength+1), "https://oss.example.com/image.png")
			},
		},
		{
			name: "list invalid limit",
			run: func() error {
				_, _, err := repo.List(ctx, 0, 0)
				return err
			},
		},
		{
			name: "list invalid offset",
			run: func() error {
				_, _, err := repo.List(ctx, 10, -1)
				return err
			},
		},
		{
			name: "discord active invalid limit",
			run: func() error {
				_, err := repo.GetDiscordActiveTasks(ctx, 0)
				return err
			},
		},
		{
			name: "stale invalid limit",
			run: func() error {
				_, err := repo.GetStaleActiveTasks(ctx, time.Now(), 0)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRepositoryAppErrorCode(t, tt.run(), apperrors.ErrCodeInvalidInput)
		})
	}
}

func TestValidateTaskForCreateTrimsTaskID(t *testing.T) {
	task := &model.Task{
		TaskID:           "  task-1  ",
		ParentTaskID:     "  parent-task  ",
		DiscordMessageID: "  message-1  ",
		Type:             model.TaskTypeImagine,
		Status:           model.TaskStatusPending,
	}

	if err := validateTaskForCreate(task); err != nil {
		t.Fatalf("validateTaskForCreate returned error: %v", err)
	}
	if task.TaskID != "task-1" {
		t.Fatalf("task_id = %q, want trimmed task-1", task.TaskID)
	}
	if task.ParentTaskID != "parent-task" {
		t.Fatalf("parent_task_id = %q, want trimmed parent-task", task.ParentTaskID)
	}
	if task.DiscordMessageID != "message-1" {
		t.Fatalf("discord_message_id = %q, want trimmed message-1", task.DiscordMessageID)
	}
}

func TestValidateTaskForStateUpdateDoesNotRequireImmutableType(t *testing.T) {
	task := &model.Task{
		TaskID:           "  task-1  ",
		Status:           model.TaskStatusProcessing,
		DiscordMessageID: "  message-1  ",
	}

	if err := validateTaskForStateUpdate(task); err != nil {
		t.Fatalf("validateTaskForStateUpdate returned error: %v", err)
	}
	if task.TaskID != "task-1" {
		t.Fatalf("task_id = %q, want trimmed task-1", task.TaskID)
	}
	if task.DiscordMessageID != "message-1" {
		t.Fatalf("discord_message_id = %q, want trimmed message-1", task.DiscordMessageID)
	}
}

func TestValidateTaskForStateUpdateClearsBlankOptionalDiscordMessageID(t *testing.T) {
	task := &model.Task{
		TaskID:           "task-1",
		Status:           model.TaskStatusProcessing,
		DiscordMessageID: "   ",
	}

	if err := validateTaskForStateUpdate(task); err != nil {
		t.Fatalf("validateTaskForStateUpdate returned error: %v", err)
	}
	if task.DiscordMessageID != "" {
		t.Fatalf("discord_message_id = %q, want empty", task.DiscordMessageID)
	}
}

func TestTaskStateUpdatesExcludeImmutableAndResultFields(t *testing.T) {
	callbackURL := "https://callback.example/hook"
	accountID := uint(9)
	task := &model.Task{
		TaskID:           "task-1",
		AccountID:        &accountID,
		ParentTaskID:     "parent-task",
		Type:             model.TaskTypeImagine,
		Status:           model.TaskStatusProcessing,
		Progress:         42,
		DiscordMessageID: "message-1",
		ImageURL:         "https://cdn.example/image.png",
		OSSImageURL:      "https://oss.example/image.png",
		ErrorMessage:     "failed",
		CallbackURL:      callbackURL,
	}

	updates := taskStateUpdates(task)

	for _, key := range []string{
		"account_id",
		"parent_task_id",
		"type",
		"prompt",
		"image_url",
		"oss_image_url",
		"error_message",
		"callback_url",
		"finished_at",
		"created_at",
		"deleted_at",
	} {
		if _, ok := updates[key]; ok {
			t.Fatalf("taskStateUpdates should not include field %q", key)
		}
	}

	for _, key := range []string{
		"discord_message_id",
		"status",
		"progress",
		"buttons",
		"updated_at",
	} {
		if _, ok := updates[key]; !ok {
			t.Fatalf("taskStateUpdates missing field %q", key)
		}
	}
}

func TestTaskStatusUpdatesRefreshUpdatedAt(t *testing.T) {
	updates := taskStatusUpdates(model.TaskStatusSubmitted)

	if updates["status"] != model.TaskStatusSubmitted {
		t.Fatalf("status = %v, want SUBMITTED", updates["status"])
	}
	assertUpdatedAtSet(t, updates)
}

func TestTaskOSSImageURLUpdatesRefreshUpdatedAt(t *testing.T) {
	updates := taskOSSImageURLUpdates("https://oss.example.com/image.png")

	if updates["oss_image_url"] != "https://oss.example.com/image.png" {
		t.Fatalf("oss_image_url = %v, want OSS URL", updates["oss_image_url"])
	}
	assertUpdatedAtSet(t, updates)
}

func TestTaskOSSImageURLUpdateQueryRequiresSuccessStatus(t *testing.T) {
	db := newDryRunAccountDB(t)

	where := queryWhereClauseText(taskOSSImageURLUpdateQuery(db.Session(&gorm.Session{DryRun: true}), "task-1"))

	for _, expected := range []string{"task_id", "status"} {
		if !strings.Contains(where, expected) {
			t.Fatalf("oss image update WHERE = %q, want %q guard", where, expected)
		}
	}
}

func TestTaskRepositoryRejectsMissingDatabase(t *testing.T) {
	repo := &taskRepository{}
	ctx := context.Background()
	validTask := func() *model.Task {
		return &model.Task{
			TaskID: "task-1",
			Type:   model.TaskTypeImagine,
			Status: model.TaskStatusPending,
		}
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "create",
			run: func() error {
				return repo.Create(ctx, validTask())
			},
		},
		{
			name: "get by task id",
			run: func() error {
				_, err := repo.GetByTaskID(ctx, "task-1")
				return err
			},
		},
		{
			name: "update",
			run: func() error {
				return repo.Update(ctx, validTask())
			},
		},
		{
			name: "update terminal",
			run: func() error {
				task := validTask()
				task.Status = model.TaskStatusFailed
				_, err := repo.UpdateTerminal(ctx, task)
				return err
			},
		},
		{
			name: "update status",
			run: func() error {
				return repo.UpdateStatus(ctx, "task-1", model.TaskStatusProcessing)
			},
		},
		{
			name: "update oss image url",
			run: func() error {
				return repo.UpdateOSSImageURL(ctx, "task-1", "https://oss.example.com/image.png")
			},
		},
		{
			name: "list",
			run: func() error {
				_, _, err := repo.List(ctx, 10, 0)
				return err
			},
		},
		{
			name: "get by discord message id",
			run: func() error {
				_, err := repo.GetByDiscordMessageID(ctx, "message-1")
				return err
			},
		},
		{
			name: "discord active",
			run: func() error {
				_, err := repo.GetDiscordActiveTasks(ctx, 10)
				return err
			},
		},
		{
			name: "stale active",
			run: func() error {
				_, err := repo.GetStaleActiveTasks(ctx, time.Now(), 10)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRepositoryAppErrorCode(t, tt.run(), apperrors.ErrCodeDatabaseError)
		})
	}
}

func TestTaskRepositoryNilReceiverRejectsMissingDatabase(t *testing.T) {
	var repo *taskRepository

	err := repo.Create(context.Background(), &model.Task{
		TaskID: "task-1",
		Type:   model.TaskTypeImagine,
		Status: model.TaskStatusPending,
	})

	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeDatabaseError)
}

func TestTaskStatusUpdateResultErrorRejectsNilResult(t *testing.T) {
	repo := &taskRepository{}

	err := repo.taskStatusUpdateResultError(context.Background(), nil, "task-1")

	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeDatabaseError)
}

func TestTerminalTaskUpdateResult(t *testing.T) {
	repo := &taskRepository{}

	transitioned, err := repo.terminalTaskUpdateResult(context.Background(), &gorm.DB{RowsAffected: 1}, "task-1")
	if err != nil {
		t.Fatalf("terminalTaskUpdateResult returned error: %v", err)
	}
	if !transitioned {
		t.Fatalf("transitioned = false, want true")
	}

	_, err = repo.terminalTaskUpdateResult(context.Background(), nil, "task-1")
	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeDatabaseError)

	_, err = repo.terminalTaskUpdateResult(context.Background(), &gorm.DB{Error: gorm.ErrRecordNotFound}, "task-1")
	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeTaskNotFound)
}

func TestOSSImageURLUpdateResultErrorRejectsNilResult(t *testing.T) {
	repo := &taskRepository{}

	err := repo.ossImageURLUpdateResultError(context.Background(), nil, "task-1")

	assertRepositoryAppErrorCode(t, err, apperrors.ErrCodeDatabaseError)
}

func assertRepositoryAppErrorCode(t *testing.T, err error, code apperrors.ErrorCode) {
	t.Helper()

	var appErr *apperrors.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %T %v, want AppError code %s", err, err, code)
	}
	if appErr.Code != code {
		t.Fatalf("code = %q, want %q", appErr.Code, code)
	}
}

func TestTerminalTaskUpdatesIncludesResultFields(t *testing.T) {
	finishedAt := time.Now()
	buttons := `["MJ::JOB::upsample::1::uuid"]`
	task := &model.Task{
		TaskID:           "task-1",
		Status:           model.TaskStatusSuccess,
		Progress:         100,
		DiscordMessageID: "message-1",
		ImageURL:         "https://example.com/image.png",
		Buttons:          &buttons,
		Description:      "first description",
		FinishedAt:       &finishedAt,
	}

	updates := terminalTaskUpdates(task)

	if updates["status"] != model.TaskStatusSuccess {
		t.Fatalf("status = %v, want SUCCESS", updates["status"])
	}
	if updates["progress"] != 100 {
		t.Fatalf("progress = %v, want 100", updates["progress"])
	}
	if updates["discord_message_id"] != "message-1" {
		t.Fatalf("discord_message_id = %v, want message-1", updates["discord_message_id"])
	}
	if updates["image_url"] != "https://example.com/image.png" {
		t.Fatalf("image_url = %v, want image URL", updates["image_url"])
	}
	if updates["buttons"] != &buttons {
		t.Fatalf("buttons pointer was not preserved")
	}
	if updates["description"] != "first description" {
		t.Fatalf("description = %v, want first description", updates["description"])
	}
	if updates["finished_at"] != &finishedAt {
		t.Fatalf("finished_at pointer was not preserved")
	}
	assertUpdatedAtSet(t, updates)
}

func TestTerminalTaskUpdatesOmitsEmptyOptionalResultFields(t *testing.T) {
	finishedAt := time.Now()
	task := &model.Task{
		TaskID:     "task-1",
		Status:     model.TaskStatusFailed,
		Progress:   72,
		FinishedAt: &finishedAt,
	}

	updates := terminalTaskUpdates(task)

	for _, key := range []string{"discord_message_id", "image_url", "buttons", "description"} {
		if _, ok := updates[key]; ok {
			t.Fatalf("terminalTaskUpdates should not include empty optional field %q", key)
		}
	}
	if updates["status"] != model.TaskStatusFailed {
		t.Fatalf("status = %v, want FAILED", updates["status"])
	}
	if updates["progress"] != 72 {
		t.Fatalf("progress = %v, want preserved failure progress", updates["progress"])
	}
	if updates["finished_at"] != &finishedAt {
		t.Fatalf("finished_at pointer was not preserved")
	}
	assertUpdatedAtSet(t, updates)
}

func TestTerminalTaskUpdatesSanitizesErrorMessage(t *testing.T) {
	task := &model.Task{
		TaskID:       "task-1",
		Status:       model.TaskStatusFailed,
		ErrorMessage: `discord API error: user_token="secret-token" callback=https://user:pass@example.com/hook?token=secret#frag`,
	}

	updates := terminalTaskUpdates(task)
	errorMessage, ok := updates["error_message"].(string)
	if !ok {
		t.Fatalf("error_message update type = %T, want string", updates["error_message"])
	}
	for _, forbidden := range []string{"secret-token", "token=secret", "user:pass", "#frag"} {
		if strings.Contains(errorMessage, forbidden) {
			t.Fatalf("error_message exposed %q: %s", forbidden, errorMessage)
		}
	}
	if !strings.Contains(errorMessage, `user_token="<redacted>"`) || !strings.Contains(errorMessage, "https://example.com/hook") {
		t.Fatalf("error_message did not keep useful redacted context: %s", errorMessage)
	}
}

func TestEnsureTaskFinishedAtSetsMissingTime(t *testing.T) {
	task := &model.Task{}

	ensureTaskFinishedAt(task)

	if task.FinishedAt == nil {
		t.Fatal("finished_at was not set")
	}
	if task.FinishedAt.IsZero() {
		t.Fatal("finished_at was zero")
	}
}

func TestEnsureTaskFinishedAtPreservesExistingTime(t *testing.T) {
	finishedAt := time.Now().Add(-time.Hour)
	task := &model.Task{FinishedAt: &finishedAt}

	ensureTaskFinishedAt(task)

	if task.FinishedAt != &finishedAt {
		t.Fatal("finished_at pointer was not preserved")
	}
}

func assertUpdatedAtSet(t *testing.T, updates map[string]interface{}) {
	t.Helper()

	value, ok := updates["updated_at"]
	if !ok {
		t.Fatal("updated_at update was missing")
	}
	updatedAt, ok := value.(time.Time)
	if !ok {
		t.Fatalf("updated_at type = %T, want time.Time", value)
	}
	if updatedAt.IsZero() {
		t.Fatal("updated_at was zero")
	}
}

func TestSanitizeTaskErrorMessageTruncatesByRune(t *testing.T) {
	longMessage := strings.Repeat("\u597d", maxTaskErrorMessageLength+1)

	got := sanitizeTaskErrorMessage(longMessage)

	if !utf8.ValidString(got) {
		t.Fatalf("sanitizeTaskErrorMessage returned invalid UTF-8")
	}
	if utf8.RuneCountInString(got) != maxTaskErrorMessageLength {
		t.Fatalf("rune count = %d, want %d", utf8.RuneCountInString(got), maxTaskErrorMessageLength)
	}
}

func TestActiveTaskStatusesExcludeTerminalStatuses(t *testing.T) {
	active := map[model.TaskStatus]bool{}
	for _, status := range model.ActiveTaskStatuses() {
		active[status] = true
	}

	for _, status := range []model.TaskStatus{
		model.TaskStatusPending,
		model.TaskStatusSubmitted,
		model.TaskStatusInQueue,
		model.TaskStatusProcessing,
	} {
		if !active[status] {
			t.Fatalf("active statuses missing %s", status)
		}
	}

	for _, status := range model.TerminalTaskStatuses() {
		if active[status] {
			t.Fatalf("terminal status %s should not be active", status)
		}
	}
}

func TestDiscordActiveTaskStatusesExcludePendingAndTerminalStatuses(t *testing.T) {
	active := map[model.TaskStatus]bool{}
	for _, status := range model.DiscordActiveTaskStatuses() {
		active[status] = true
	}

	if active[model.TaskStatusPending] {
		t.Fatal("pending task should not be treated as Discord-active before submission")
	}

	for _, status := range []model.TaskStatus{
		model.TaskStatusSubmitted,
		model.TaskStatusInQueue,
		model.TaskStatusProcessing,
	} {
		if !active[status] {
			t.Fatalf("Discord-active statuses missing %s", status)
		}
	}

	for _, status := range model.TerminalTaskStatuses() {
		if active[status] {
			t.Fatalf("terminal status %s should not be Discord-active", status)
		}
	}
}
