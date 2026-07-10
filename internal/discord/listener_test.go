package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/trae/midjourney-api/internal/model"
	"github.com/trae/midjourney-api/pkg/constants"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

func TestUpdateTaskIgnoresTerminalTask(t *testing.T) {
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID:   "task-1",
		Status:   model.TaskStatusSuccess,
		Progress: 100,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-1",
		Status:    "processing",
		Progress:  42,
	})

	if task.Status != model.TaskStatusSuccess {
		t.Fatalf("status = %q, want SUCCESS", task.Status)
	}
	if task.Progress != 100 {
		t.Fatalf("progress = %d, want 100", task.Progress)
	}
	if repo.updateCalled || repo.updateTerminalCalled {
		t.Fatalf("terminal task should not be written again")
	}
}

func TestUpdateTaskUsesTerminalUpdateForCompletion(t *testing.T) {
	repo := &fakeListenerTaskRepo{terminalTransitioned: true}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID: "task-2",
		Status: model.TaskStatusProcessing,
		Type:   model.TaskTypeImagine,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-2",
		Status:    "completed",
		Progress:  100,
		ImageURL:  "https://example.com/image.png",
	})

	if !repo.updateTerminalCalled {
		t.Fatalf("expected UpdateTerminal to be called")
	}
	if repo.updateCalled {
		t.Fatalf("ordinary Update should not be used for terminal transitions")
	}
	if task.Status != model.TaskStatusSuccess {
		t.Fatalf("status = %q, want SUCCESS", task.Status)
	}
}

func TestUpdateTaskLogsRedactedImageURL(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	repo := &fakeListenerTaskRepo{terminalTransitioned: true}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.New(core),
	}
	task := &model.Task{
		TaskID: "task-redacted-image",
		Status: model.TaskStatusProcessing,
		Type:   model.TaskTypeImagine,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-redacted-image",
		Status:    "completed",
		Progress:  100,
		ImageURL:  "https://user:secret@example.com/image.png?token=secret-token#fragment",
	})

	var imageURL string
	for _, entry := range logs.All() {
		if entry.Message != "[Completed]" {
			continue
		}
		value, ok := entry.ContextMap()["Image URL"].(string)
		if !ok {
			t.Fatalf("Image URL field missing or wrong type: %#v", entry.ContextMap())
		}
		imageURL = value
		break
	}
	if imageURL == "" {
		t.Fatal("completed log entry was not found")
	}
	if strings.Contains(imageURL, "secret") ||
		strings.Contains(imageURL, "token=") ||
		strings.Contains(imageURL, "fragment") ||
		strings.Contains(imageURL, "user:") {
		t.Fatalf("image URL was not redacted: %s", imageURL)
	}
	if imageURL != "https://example.com/image.png" {
		t.Fatalf("image URL = %q, want redacted path", imageURL)
	}
}

func TestUpdateTaskRecordsAccountFailureForTerminalFailure(t *testing.T) {
	accountID := uint(13)
	taskRepo := &fakeListenerTaskRepo{terminalTransitioned: true}
	accountRepo := &fakeListenerAccountRepo{}
	listener := &Listener{
		taskRepo:    taskRepo,
		accountRepo: accountRepo,
		logger:      zap.NewNop(),
	}
	task := &model.Task{
		TaskID:    "task-failed",
		Status:    model.TaskStatusProcessing,
		Type:      model.TaskTypeImagine,
		AccountID: &accountID,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-failed",
		Status:    "failed",
	})

	if !taskRepo.updateTerminalCalled {
		t.Fatalf("expected UpdateTerminal to be called")
	}
	if task.Status != model.TaskStatusFailed {
		t.Fatalf("status = %q, want FAILED", task.Status)
	}
	if task.ErrorMessage == "" {
		t.Fatalf("error message was empty")
	}
	if len(accountRepo.decrementedIDs) != 1 || accountRepo.decrementedIDs[0] != accountID {
		t.Fatalf("decrementedIDs = %#v, want account %d", accountRepo.decrementedIDs, accountID)
	}
	if len(accountRepo.recordedResults) != 1 {
		t.Fatalf("recordedResults = %#v, want one failure", accountRepo.recordedResults)
	}
	if accountRepo.recordedResults[0].success {
		t.Fatalf("recorded result success = true, want false")
	}
	if accountRepo.recordedResults[0].lastError == "" {
		t.Fatalf("recorded lastError was empty")
	}
}

func TestUpdateTaskPreservesProgressWhenFailureHasNoProgress(t *testing.T) {
	taskRepo := &fakeListenerTaskRepo{terminalTransitioned: true}
	listener := &Listener{
		taskRepo: taskRepo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID:   "task-failed-progress",
		Status:   model.TaskStatusProcessing,
		Type:     model.TaskTypeImagine,
		Progress: 72,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-failed-progress",
		Status:    "failed",
		Progress:  0,
	})

	if !taskRepo.updateTerminalCalled {
		t.Fatalf("expected UpdateTerminal to be called")
	}
	if task.Status != model.TaskStatusFailed {
		t.Fatalf("status = %q, want FAILED", task.Status)
	}
	if task.Progress != 72 {
		t.Fatalf("progress = %d, want previous progress 72", task.Progress)
	}
}

func TestUpdateTaskUsesParsedProgressForFailureWhenPresent(t *testing.T) {
	taskRepo := &fakeListenerTaskRepo{terminalTransitioned: true}
	listener := &Listener{
		taskRepo: taskRepo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID:   "task-failed-progress-present",
		Status:   model.TaskStatusProcessing,
		Type:     model.TaskTypeImagine,
		Progress: 24,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-failed-progress-present",
		Status:    "failed",
		Progress:  48,
	})

	if !taskRepo.updateTerminalCalled {
		t.Fatalf("expected UpdateTerminal to be called")
	}
	if task.Progress != 48 {
		t.Fatalf("progress = %d, want parsed progress 48", task.Progress)
	}
}

func TestUpdateTaskRecordsAccountSuccessForTerminalSuccess(t *testing.T) {
	accountID := uint(13)
	taskRepo := &fakeListenerTaskRepo{terminalTransitioned: true}
	accountRepo := &fakeListenerAccountRepo{}
	listener := &Listener{
		taskRepo:    taskRepo,
		accountRepo: accountRepo,
		logger:      zap.NewNop(),
	}
	task := &model.Task{
		TaskID:    "task-success",
		Status:    model.TaskStatusProcessing,
		Type:      model.TaskTypeImagine,
		AccountID: &accountID,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-success",
		Status:    "completed",
		Progress:  100,
		ImageURL:  "https://example.com/image.png",
	})

	if !taskRepo.updateTerminalCalled {
		t.Fatalf("expected UpdateTerminal to be called")
	}
	if len(accountRepo.decrementedIDs) != 1 || accountRepo.decrementedIDs[0] != accountID {
		t.Fatalf("decrementedIDs = %#v, want account %d", accountRepo.decrementedIDs, accountID)
	}
	if len(accountRepo.recordedResults) != 1 {
		t.Fatalf("recordedResults = %#v, want one success", accountRepo.recordedResults)
	}
	if !accountRepo.recordedResults[0].success {
		t.Fatalf("recorded result success = false, want true")
	}
	if accountRepo.recordedResults[0].lastError != "" {
		t.Fatalf("recorded lastError = %q, want empty", accountRepo.recordedResults[0].lastError)
	}
}

func TestUpdateTaskIgnoresUnknownMessageWithoutButtons(t *testing.T) {
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID:   "task-unknown",
		Status:   model.TaskStatusProcessing,
		Progress: 42,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-unknown",
		Status:    "unknown",
		Progress:  0,
	})

	if task.Status != model.TaskStatusProcessing {
		t.Fatalf("status = %q, want PROCESSING", task.Status)
	}
	if task.Progress != 42 {
		t.Fatalf("progress = %d, want 42", task.Progress)
	}
	if repo.updateCalled || repo.updateTerminalCalled {
		t.Fatal("unknown message without buttons should not write task")
	}
}

func TestUpdateTaskAllowsMissingAccountRepoForLogContext(t *testing.T) {
	accountID := uint(7)
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID:    "task-no-account-repo",
		Status:    model.TaskStatusSubmitted,
		AccountID: &accountID,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-processing",
		Status:    "processing",
		Progress:  25,
	})

	if !repo.updateCalled {
		t.Fatal("expected task update")
	}
	if task.Status != model.TaskStatusProcessing {
		t.Fatalf("status = %q, want PROCESSING", task.Status)
	}
}

func TestUpdateTaskAllowsNilAccountLookupForLogContext(t *testing.T) {
	accountID := uint(7)
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		taskRepo:    repo,
		accountRepo: &fakeListenerAccountRepo{returnNilAccount: true},
		logger:      zap.NewNop(),
	}
	task := &model.Task{
		TaskID:    "task-nil-account",
		Status:    model.TaskStatusSubmitted,
		AccountID: &accountID,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-processing",
		Status:    "processing",
		Progress:  25,
	})

	if !repo.updateCalled {
		t.Fatal("expected task update")
	}
	if task.Status != model.TaskStatusProcessing {
		t.Fatalf("status = %q, want PROCESSING", task.Status)
	}
}

func TestUpdateTaskUsesDeadlineForRepositoryWrite(t *testing.T) {
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID: "task-deadline",
		Status: model.TaskStatusSubmitted,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-deadline",
		Status:    "processing",
		Progress:  25,
	})

	if !repo.updateCalled {
		t.Fatal("expected task update")
	}
	if !repo.updateSawDeadline {
		t.Fatal("task repository update did not receive a deadline")
	}
}

func TestUpdateTaskSkipsNoopProcessingMessage(t *testing.T) {
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID:   "task-noop-processing",
		Status:   model.TaskStatusProcessing,
		Progress: 42,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-noop-processing",
		Status:    "processing",
		Progress:  42,
	})

	if repo.updateCalled || repo.updateTerminalCalled {
		t.Fatal("unchanged processing message should not write task")
	}
	if task.Status != model.TaskStatusProcessing || task.Progress != 42 {
		t.Fatalf("task changed on noop message: %#v", task)
	}
}

func TestUpdateTaskDoesNotRegressProcessingToPending(t *testing.T) {
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID:   "task-no-regress",
		Status:   model.TaskStatusProcessing,
		Progress: 55,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-late-pending",
		Status:    "pending",
		Progress:  0,
	})

	if repo.updateCalled || repo.updateTerminalCalled {
		t.Fatal("late pending message should not write task")
	}
	if task.Status != model.TaskStatusProcessing || task.Progress != 55 {
		t.Fatalf("task regressed on late pending message: %#v", task)
	}
}

func TestUpdateTaskSkipsUnchangedButtons(t *testing.T) {
	buttons := `["button-1"]`
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID:  "task-same-buttons",
		Status:  model.TaskStatusProcessing,
		Buttons: &buttons,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-same-buttons",
		Status:    "unknown",
		Buttons:   []string{"button-1"},
	})

	if repo.updateCalled || repo.updateTerminalCalled {
		t.Fatal("unchanged buttons should not write task")
	}
}

func TestUpdateTaskPersistsChangedButtons(t *testing.T) {
	buttons := `["button-1"]`
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		taskRepo: repo,
		logger:   zap.NewNop(),
	}
	task := &model.Task{
		TaskID:  "task-new-buttons",
		Status:  model.TaskStatusProcessing,
		Buttons: &buttons,
	}

	listener.updateTask(task, &ParsedMessage{
		MessageID: "message-new-buttons",
		Status:    "unknown",
		Buttons:   []string{"button-1", "button-2"},
	})

	if !repo.updateCalled {
		t.Fatal("changed buttons should write task")
	}
	if task.Buttons == nil || *task.Buttons != `["button-1","button-2"]` {
		t.Fatalf("buttons = %#v, want updated JSON", task.Buttons)
	}
}

func TestMarkMessageMatchedInitializesMap(t *testing.T) {
	listener := &Listener{}

	listener.markMessageMatched("message-1")

	if !listener.isMessageMatched("message-1") {
		t.Fatal("message should be marked as matched")
	}
}

func TestSendCallbackWithRetryRetriesServerErrors(t *testing.T) {
	var attempts atomic.Int32
	listener := &Listener{
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("content-type = %q, want application/json", got)
				}
				if attempts.Add(1) < 3 {
					return callbackResponse(http.StatusInternalServerError), nil
				}
				return callbackResponse(http.StatusNoContent), nil
			}),
		},
		logger: zap.NewNop(),
	}

	listener.sendCallbackWithRetry(context.Background(), "https://callback.example.com/hook", &model.Task{TaskID: "task-3"})

	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestSendCallbackWithRetryDoesNotRetryClientErrors(t *testing.T) {
	var attempts atomic.Int32
	listener := &Listener{
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				attempts.Add(1)
				return callbackResponse(http.StatusBadRequest), nil
			}),
		},
		logger: zap.NewNop(),
	}

	listener.sendCallbackWithRetry(context.Background(), "https://callback.example.com/hook", &model.Task{TaskID: "task-4"})

	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestSendCallbackUsesNormalizedRequestURL(t *testing.T) {
	var requestedURL string
	listener := &Listener{
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requestedURL = req.URL.String()
				return callbackResponse(http.StatusNoContent), nil
			}),
		},
		logger: zap.NewNop(),
	}

	retry, err := listener.sendCallback(
		context.Background(),
		"  https://callback.example.com/a hook?token=secret  ",
		&model.Task{TaskID: "task-normalized-callback"},
	)
	if err != nil {
		t.Fatalf("sendCallback returned error: %v", err)
	}
	if retry {
		t.Fatal("retry = true, want false")
	}
	if requestedURL != "https://callback.example.com/a%20hook?token=secret" {
		t.Fatalf("request URL = %q, want normalized URL", requestedURL)
	}
}

func TestSendCallbackLogsRedactedCallbackURL(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	listener := &Listener{
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return callbackResponse(http.StatusBadRequest), nil
			}),
		},
		logger: zap.New(core),
	}

	listener.sendCallbackWithRetry(
		context.Background(),
		"https://callback.example.com/callback?token=secret-token#fragment",
		&model.Task{TaskID: "task-redacted-callback"},
	)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	callbackURL, ok := entries[0].ContextMap()["callback_url"].(string)
	if !ok {
		t.Fatalf("callback_url field missing or wrong type: %#v", entries[0].ContextMap())
	}
	if strings.Contains(callbackURL, "secret-token") ||
		strings.Contains(callbackURL, "token=") ||
		strings.Contains(callbackURL, "fragment") {
		t.Fatalf("callback_url was not redacted: %s", callbackURL)
	}
	if !strings.HasSuffix(callbackURL, "/callback") {
		t.Fatalf("callback_url = %q, want path preserved", callbackURL)
	}
}

func TestUploadOSSAndCallbackSkipsFallbackCallbackForNonSuccessTask(t *testing.T) {
	updateDone := make(chan struct{})
	callbackCalled := make(chan struct{}, 1)
	repo := &fakeListenerTaskRepo{
		getByTaskIDTask: &model.Task{
			TaskID: "task-oss-stale",
			Status: model.TaskStatusFailed,
		},
		updateOSSErr: errors.New("stale task"),
		updateOSSCh:  updateDone,
	}
	listener := &Listener{
		taskRepo: repo,
		ossUploader: &fakeListenerOSSUploader{
			url: "https://oss.example.com/image.png",
		},
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				callbackCalled <- struct{}{}
				return callbackResponse(http.StatusNoContent), nil
			}),
		},
		logger: zap.NewNop(),
	}

	listener.uploadOSSAndCallback(&model.Task{
		TaskID:      "task-oss-stale",
		Status:      model.TaskStatusSuccess,
		ImageURL:    "https://cdn.example.com/image.png",
		CallbackURL: "https://callback.example.com/hook",
	})

	select {
	case <-updateDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OSS database update")
	}

	select {
	case <-callbackCalled:
		t.Fatal("callback was sent with OSS URL for a non-success task")
	case <-time.After(25 * time.Millisecond):
	}
	if repo.getByTaskIDTask.OSSImageURL != "" {
		t.Fatalf("latest task OSSImageURL = %q, want unchanged", repo.getByTaskIDTask.OSSImageURL)
	}
}

func TestUploadOSSAndCallbackFallsBackToTaskSnapshotWhenUploadFailsAndReloadFails(t *testing.T) {
	callbackBody := make(chan []byte, 1)
	repo := &fakeListenerTaskRepo{
		getByTaskIDErr: errors.New("database unavailable"),
	}
	listener := &Listener{
		taskRepo: repo,
		ossUploader: &fakeListenerOSSUploader{
			err: errors.New("upload unavailable"),
		},
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Errorf("read callback body: %v", err)
					return callbackResponse(http.StatusInternalServerError), nil
				}
				callbackBody <- body
				return callbackResponse(http.StatusNoContent), nil
			}),
		},
		logger: zap.NewNop(),
	}

	listener.uploadOSSAndCallback(&model.Task{
		TaskID:      "task-oss-upload-fallback",
		Type:        model.TaskTypeImagine,
		Status:      model.TaskStatusSuccess,
		Progress:    constants.MaxTaskProgress,
		ImageURL:    "https://cdn.example.com/image.png",
		CallbackURL: "https://callback.example.com/hook",
	})

	body := waitForCallbackBody(t, callbackBody)
	assertCallbackTaskField(t, body, "task_id", "task-oss-upload-fallback")
	if strings.Contains(string(body), `"oss_image_url"`) {
		t.Fatalf("callback body included oss_image_url after upload failure: %s", body)
	}
}

func TestUploadOSSAndCallbackFallsBackToTaskSnapshotWhenOSSWriteFailsAndReloadFails(t *testing.T) {
	updateDone := make(chan struct{})
	callbackBody := make(chan []byte, 1)
	repo := &fakeListenerTaskRepo{
		getByTaskIDErr: errors.New("database unavailable"),
		updateOSSErr:   errors.New("database write unavailable"),
		updateOSSCh:    updateDone,
	}
	listener := &Listener{
		taskRepo: repo,
		ossUploader: &fakeListenerOSSUploader{
			url: "https://oss.example.com/image.png",
		},
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Errorf("read callback body: %v", err)
					return callbackResponse(http.StatusInternalServerError), nil
				}
				callbackBody <- body
				return callbackResponse(http.StatusNoContent), nil
			}),
		},
		logger: zap.NewNop(),
	}

	listener.uploadOSSAndCallback(&model.Task{
		TaskID:      "task-oss-write-fallback",
		Type:        model.TaskTypeImagine,
		Status:      model.TaskStatusSuccess,
		Progress:    constants.MaxTaskProgress,
		ImageURL:    "https://cdn.example.com/image.png",
		CallbackURL: "https://callback.example.com/hook",
	})

	select {
	case <-updateDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for OSS database update")
	}

	body := waitForCallbackBody(t, callbackBody)
	assertCallbackTaskField(t, body, "task_id", "task-oss-write-fallback")
	assertCallbackTaskField(t, body, "oss_image_url", "https://oss.example.com/image.png")
}

func waitForCallbackBody(t *testing.T, body <-chan []byte) []byte {
	t.Helper()

	select {
	case got := <-body:
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback")
		return nil
	}
}

func assertCallbackTaskField(t *testing.T, body []byte, field string, want string) {
	t.Helper()

	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal callback body: %v", err)
	}
	if got := payload.Data[field]; got != want {
		t.Fatalf("%s = %#v, want %q; body=%s", field, got, want, body)
	}
}

type fakeListenerOSSUploader struct {
	url string
	err error
}

func (u *fakeListenerOSSUploader) UploadFromURL(ctx context.Context, taskID string, imageURL string) (string, error) {
	return u.url, u.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func callbackResponse(statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

func TestSendCallbackLogsRedactedTransportError(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	listener := &Listener{
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, errors.New(`callback failed: https://user:pass@example.com/hook?token=secret#frag custom_id="secret-custom-id"`)
			}),
		},
		logger: zap.New(core),
	}

	listener.sendCallbackWithRetry(
		context.Background(),
		"https://example.com/hook?token=secret#frag",
		&model.Task{TaskID: "task-redacted-error"},
	)

	rendered := fmt.Sprint(logs.AllUntimed())
	for _, forbidden := range []string{
		"token=secret",
		"user:pass",
		"#frag",
		"custom_id",
		"secret-custom-id",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("callback logs exposed %q: %s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "https://example.com/hook") || !strings.Contains(rendered, "<redacted>") {
		t.Fatalf("callback logs did not keep useful redacted context: %s", rendered)
	}
}

func TestSendCallbackRejectsPrivateIPBeforeHTTPClient(t *testing.T) {
	called := false
	listener := &Listener{
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				called = true
				return nil, errors.New("transport should not be called")
			}),
		},
		logger: zap.NewNop(),
	}

	retry, err := listener.sendCallback(
		context.Background(),
		"http://127.0.0.1/callback?token=secret-token#fragment",
		&model.Task{TaskID: "task-private-callback"},
	)

	if err == nil {
		t.Fatal("expected private network rejection")
	}
	if retry {
		t.Fatal("retry = true, want false for private/local callback target")
	}
	if called {
		t.Fatal("callback HTTP client was called for a private IP literal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "private or local address") {
		t.Fatalf("error = %q, want private/local context", msg)
	}
	for _, forbidden := range []string{"secret-token", "token=", "fragment"} {
		if strings.Contains(msg, forbidden) {
			t.Fatalf("callback error exposed %q: %s", forbidden, msg)
		}
	}
}

func TestSendCallbackRejectsNilTaskBeforeHTTPClient(t *testing.T) {
	called := false
	listener := &Listener{
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				called = true
				return callbackResponse(http.StatusNoContent), nil
			}),
		},
		logger: zap.NewNop(),
	}

	retry, err := listener.sendCallback(context.Background(), "https://callback.example.com/hook", nil)

	if err == nil {
		t.Fatal("expected nil task rejection")
	}
	if retry {
		t.Fatal("retry = true, want false for invalid callback input")
	}
	if called {
		t.Fatal("callback HTTP client was called for nil task")
	}
	if !strings.Contains(err.Error(), "callback task is required") {
		t.Fatalf("error = %q, want nil task context", err.Error())
	}
}

func TestSendCallbackWithRetryHandlesNilTaskWithoutPanic(t *testing.T) {
	listener := &Listener{logger: zap.NewNop()}

	listener.sendCallbackWithRetry(context.Background(), "https://callback.example.com/hook", nil)
}

func TestDefaultCallbackClientRejectsPrivateNetworkAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("private-network callback unexpectedly reached test server")
	}))
	defer server.Close()

	listener := &Listener{
		callbackClient: newCallbackHTTPClient(),
		logger:         zap.NewNop(),
	}

	callbackURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	retry, err := listener.sendCallback(
		context.Background(),
		callbackURL+"/callback?token=secret-token#fragment",
		&model.Task{TaskID: "task-private-callback"},
	)
	if err == nil {
		t.Fatal("expected private network rejection")
	}
	if retry {
		t.Fatal("retry = true, want false for private/local callback target")
	}

	msg := err.Error()
	if !strings.Contains(msg, "private or local address") {
		t.Fatalf("error = %q, want private/local context", msg)
	}
	if strings.Contains(msg, "secret-token") || strings.Contains(msg, "token=") || strings.Contains(msg, "fragment") {
		t.Fatalf("callback error exposed sensitive URL parts: %s", msg)
	}
}

func TestSendCallbackUsesPublicTaskView(t *testing.T) {
	accountID := uint(13)
	buttons := `[{"custom_id":"secret-button-id"}]`
	finishedAt := time.Now()
	var received []byte

	listener := &Listener{
		callbackClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("content-type = %q, want application/json", got)
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
					return callbackResponse(http.StatusInternalServerError), nil
				}
				received = body
				return callbackResponse(http.StatusNoContent), nil
			}),
		},
		logger: zap.NewNop(),
	}
	retry, err := listener.sendCallback(context.Background(), "https://callback.example.com/hook", &model.Task{
		ID:               42,
		TaskID:           "task-public",
		AccountID:        &accountID,
		ParentTaskID:     "parent-public",
		Type:             model.TaskTypeImagine,
		Prompt:           "a quiet lake",
		Status:           model.TaskStatusSuccess,
		Progress:         100,
		DiscordMessageID: "message-secret",
		ImageURL:         "https://cdn.example.com/image.png",
		OSSImageURL:      "https://oss.example.com/image.png",
		ErrorMessage:     `user_token="secret-token" callback=https://example.com/hook?token=secret#frag`,
		Buttons:          &buttons,
		Description:      "done",
		CallbackURL:      "https://callback.example.com/hook?token=secret",
		CreatedAt:        time.Now().Add(-time.Minute),
		UpdatedAt:        time.Now(),
		FinishedAt:       &finishedAt,
	})
	if err != nil {
		t.Fatalf("sendCallback returned error: %v", err)
	}
	if retry {
		t.Fatal("retry = true, want false")
	}

	body := string(received)
	for _, forbidden := range []string{
		`"id":`,
		`"account_id"`,
		`"discord_message_id"`,
		`"buttons"`,
		`"callback_url"`,
		"message-secret",
		"secret-button-id",
		"secret-token",
		"token=secret",
		"#frag",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("callback body exposed %q: %s", forbidden, body)
		}
	}

	var payload struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(received, &payload); err != nil {
		t.Fatalf("unmarshal callback body: %v", err)
	}
	if payload.Code != "SUCCESS" || payload.Message != "success" {
		t.Fatalf("callback envelope = %#v", payload)
	}
	if payload.Data["task_id"] != "task-public" {
		t.Fatalf("task_id = %#v, want task-public", payload.Data["task_id"])
	}
	if payload.Data["prompt"] != "a quiet lake" {
		t.Fatalf("prompt = %#v, want public prompt", payload.Data["prompt"])
	}
	if _, ok := payload.Data["id"]; ok {
		t.Fatalf("data contained internal id: %#v", payload.Data)
	}
	errorMessage, ok := payload.Data["error_message"].(string)
	if !ok {
		t.Fatalf("error_message missing or wrong type: %#v", payload.Data["error_message"])
	}
	if !strings.Contains(errorMessage, "<redacted>") {
		t.Fatalf("error_message was not redacted: %s", errorMessage)
	}
}

func TestListenerNilCallbackClientUsesTimeoutFallback(t *testing.T) {
	listener := &Listener{}

	got := listener.httpClient()
	if got == nil {
		t.Fatal("fallback HTTP client was nil")
	}
	if got == http.DefaultClient {
		t.Fatal("fallback HTTP client must not be http.DefaultClient without a timeout")
	}
	if got.Timeout != constants.CallbackTimeout {
		t.Fatalf("fallback timeout = %s, want %s", got.Timeout, constants.CallbackTimeout)
	}
}

func TestListenerStopAllowsNilReceiverAndSession(t *testing.T) {
	var nilListener *Listener
	if err := nilListener.Stop(); err != nil {
		t.Fatalf("nil listener Stop returned error: %v", err)
	}

	listener := &Listener{}
	if err := listener.Stop(); err != nil {
		t.Fatalf("nil session Stop returned error: %v", err)
	}
}

func TestNewListenerAllowsNilLogger(t *testing.T) {
	listener := NewListener("token", "midjourney-bot", &gorm.DB{}, nil, nil)

	if listener == nil {
		t.Fatal("NewListener returned nil")
	}
	if listener.logger == nil {
		t.Fatal("listener logger was nil")
	}
	if listener.parser == nil || listener.parser.logger == nil {
		t.Fatal("listener parser logger was nil")
	}
}

func TestNewListenerRejectsNilDatabase(t *testing.T) {
	listener := NewListener("token", "midjourney-bot", nil, zap.NewNop(), nil)

	if listener != nil {
		t.Fatalf("listener = %#v, want nil", listener)
	}
}

func TestListenerStartRejectsUninitializedListener(t *testing.T) {
	var nilListener *Listener
	if err := nilListener.Start(); err == nil {
		t.Fatal("nil listener Start returned nil error")
	}

	listener := &Listener{}
	if err := listener.Start(); err == nil {
		t.Fatal("nil session Start returned nil error")
	}
}

func TestListenerHandlersIgnoreIncompleteEvents(t *testing.T) {
	var nilListener *Listener
	nilListener.handleMessageCreate(nil, nil)
	nilListener.handleMessageUpdate(nil, nil)
	nilListener.matchDescribeTask("channel-1", &ParsedMessage{MessageID: "message-1"})

	listener := &Listener{
		parser:          NewMessageParser("midjourney-bot", zap.NewNop()),
		taskRepo:        &fakeListenerTaskRepo{},
		accountRepo:     &fakeListenerAccountRepo{},
		logger:          zap.NewNop(),
		midjourneyBotID: "midjourney-bot",
	}

	listener.handleMessageCreate(nil, nil)
	listener.handleMessageCreate(nil, &discordgo.MessageCreate{})
	listener.handleMessageUpdate(nil, nil)
	listener.handleMessageUpdate(nil, &discordgo.MessageUpdate{})
	listener.updateMatchingTask(nil, nil)
	listener.matchDescribeTask("channel-1", nil)
	listener.matchDescribeTask("   ", &ParsedMessage{MessageID: "message-1", IsDescribeResult: true})
	listener.matchDescribeTask("channel-1", &ParsedMessage{MessageID: "   ", IsDescribeResult: true})
	if listener.matchActionTaskByReference("parent-message", nil) {
		t.Fatal("matchActionTaskByReference returned true for nil parsed message")
	}
}

func TestListenerHandlersIgnoreEventsWithoutMessageID(t *testing.T) {
	repo := &fakeListenerTaskRepo{}
	listener := &Listener{
		parser:          NewMessageParser("midjourney-bot", zap.NewNop()),
		taskRepo:        repo,
		accountRepo:     &fakeListenerAccountRepo{},
		logger:          zap.NewNop(),
		midjourneyBotID: "midjourney-bot",
	}
	msg := &discordgo.Message{
		ID:      "   ",
		Content: "**a quiet harbor** (100%)",
		Author:  &discordgo.User{ID: "midjourney-bot"},
	}

	listener.handleMessageCreate(nil, &discordgo.MessageCreate{Message: msg})
	listener.handleMessageUpdate(nil, &discordgo.MessageUpdate{Message: msg})
	listener.updateMatchingTask(&ParsedMessage{
		MessageID:  "   ",
		TaskPrompt: "a quiet harbor",
	}, &discordgo.Message{ID: "   ", ChannelID: "channel-1"})
	listener.matchActionTaskByReference("   ", &ParsedMessage{MessageID: "message-1"})
	listener.matchActionTaskByReference("parent-message", &ParsedMessage{MessageID: "   "})

	if repo.getByDiscordMessageIDCalls != 0 {
		t.Fatalf("GetByDiscordMessageID calls = %d, want 0", repo.getByDiscordMessageIDCalls)
	}
	if repo.discordActiveCalls != 0 {
		t.Fatalf("GetDiscordActiveTasks calls = %d, want 0", repo.discordActiveCalls)
	}
	if repo.updateCalled || repo.updateTerminalCalled {
		t.Fatalf("listener should not update tasks for events without message id")
	}
}

func TestListenerMatchingSkipsNilDiscordActiveTasks(t *testing.T) {
	repo := &fakeListenerTaskRepo{
		discordActiveTasks: []*model.Task{nil},
	}
	listener := &Listener{
		parser:          NewMessageParser("midjourney-bot", zap.NewNop()),
		taskRepo:        repo,
		accountRepo:     &fakeListenerAccountRepo{},
		logger:          zap.NewNop(),
		midjourneyBotID: "midjourney-bot",
	}

	listener.updateMatchingTask(&ParsedMessage{
		MessageID:  "message-1",
		TaskPrompt: "a quiet harbor",
	}, &discordgo.Message{ID: "message-1", ChannelID: "channel-1"})
	listener.matchDescribeTask("channel-1", &ParsedMessage{
		MessageID:        "message-2",
		IsDescribeResult: true,
	})
}

func TestListenerMatchingLogsDoNotIncludePromptText(t *testing.T) {
	accountID := uint(7)
	core, logs := observer.New(zap.DebugLevel)
	repo := &fakeListenerTaskRepo{
		discordActiveTasks: []*model.Task{
			{
				TaskID:    "task-secret-prompt",
				AccountID: &accountID,
				Type:      model.TaskTypeImagine,
				Prompt:    "database secret prompt",
				Status:    model.TaskStatusSubmitted,
				CreatedAt: time.Now(),
			},
		},
	}
	listener := &Listener{
		parser:          NewMessageParser("midjourney-bot", zap.NewNop()),
		taskRepo:        repo,
		accountRepo:     &fakeListenerAccountRepo{},
		logger:          zap.New(core),
		midjourneyBotID: "midjourney-bot",
		matchedMsgIDs:   make(map[string]time.Time),
	}

	listener.updateMatchingTask(&ParsedMessage{
		MessageID:  "message-1",
		TaskPrompt: "discord secret prompt",
	}, &discordgo.Message{ID: "message-1", ChannelID: "channel-1"})

	rendered := fmt.Sprint(logs.AllUntimed())
	for _, forbidden := range []string{"database secret prompt", "discord secret prompt"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("listener logs exposed %q: %s", forbidden, rendered)
		}
	}

	entries := logs.FilterMessage("[Imagine task match failed]").All()
	if len(entries) != 1 {
		t.Fatalf("match failed logs = %d, want 1", len(entries))
	}
	contextMap := entries[0].ContextMap()
	if _, ok := contextMap["parsed_prompt_length"]; !ok {
		t.Fatalf("match failed log missing parsed_prompt_length: %#v", contextMap)
	}
	if _, ok := contextMap["task_prompt_length"]; !ok {
		t.Fatalf("match failed log missing task_prompt_length: %#v", contextMap)
	}
}

type fakeListenerTaskRepo struct {
	updateCalled               bool
	updateTerminalCalled       bool
	updateSawDeadline          bool
	updateTerminalSawDeadline  bool
	terminalTransitioned       bool
	getByTaskIDTask            *model.Task
	getByTaskIDErr             error
	updateOSSErr               error
	updateOSSCh                chan struct{}
	discordActiveTasks         []*model.Task
	getByDiscordMessageIDCalls int
	discordActiveCalls         int
}

func (r *fakeListenerTaskRepo) Create(ctx context.Context, task *model.Task) error {
	return nil
}

func (r *fakeListenerTaskRepo) GetByTaskID(ctx context.Context, taskID string) (*model.Task, error) {
	return r.getByTaskIDTask, r.getByTaskIDErr
}

func (r *fakeListenerTaskRepo) Update(ctx context.Context, task *model.Task) error {
	r.updateCalled = true
	r.updateSawDeadline = contextHasDeadline(ctx)
	return nil
}

func (r *fakeListenerTaskRepo) UpdateTerminal(ctx context.Context, task *model.Task) (bool, error) {
	r.updateTerminalCalled = true
	r.updateTerminalSawDeadline = contextHasDeadline(ctx)
	return r.terminalTransitioned, nil
}

func (r *fakeListenerTaskRepo) UpdateStatus(ctx context.Context, taskID string, status model.TaskStatus) error {
	return nil
}

func (r *fakeListenerTaskRepo) UpdateOSSImageURL(ctx context.Context, taskID string, ossURL string) error {
	if r.updateOSSCh != nil {
		close(r.updateOSSCh)
		r.updateOSSCh = nil
	}
	return r.updateOSSErr
}

func (r *fakeListenerTaskRepo) List(ctx context.Context, limit, offset int) ([]model.Task, int64, error) {
	return nil, 0, nil
}

func (r *fakeListenerTaskRepo) GetByDiscordMessageID(ctx context.Context, messageID string) (*model.Task, error) {
	r.getByDiscordMessageIDCalls++
	return nil, nil
}

func (r *fakeListenerTaskRepo) GetDiscordActiveTasks(ctx context.Context, limit int) ([]*model.Task, error) {
	r.discordActiveCalls++
	return r.discordActiveTasks, nil
}

func (r *fakeListenerTaskRepo) GetStaleActiveTasks(ctx context.Context, cutoff time.Time, limit int) ([]*model.Task, error) {
	return nil, nil
}

type fakeListenerAccountRepo struct {
	decrementedIDs   []uint
	recordedResults  []listenerRecordedResult
	returnNilAccount bool
}

type listenerRecordedResult struct {
	accountID uint
	success   bool
	lastError string
}

func (r *fakeListenerAccountRepo) Create(ctx context.Context, account *model.Account) error {
	return nil
}

func (r *fakeListenerAccountRepo) GetByID(ctx context.Context, id uint) (*model.Account, error) {
	if r.returnNilAccount {
		return nil, nil
	}
	return &model.Account{
		ID:        id,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "token",
		IsHealthy: true,
	}, nil
}

func (r *fakeListenerAccountRepo) AcquireAvailable(ctx context.Context) (*model.Account, error) {
	return nil, nil
}

func (r *fakeListenerAccountRepo) AcquireByID(ctx context.Context, id uint) (*model.Account, error) {
	return nil, nil
}

func (r *fakeListenerAccountRepo) DecrementJobs(ctx context.Context, id uint) error {
	r.decrementedIDs = append(r.decrementedIDs, id)
	return nil
}

func (r *fakeListenerAccountRepo) List(ctx context.Context) ([]model.Account, error) {
	return nil, nil
}

func (r *fakeListenerAccountRepo) UpdateConfig(ctx context.Context, account *model.Account, resetRuntime bool) error {
	return nil
}

func (r *fakeListenerAccountRepo) Delete(ctx context.Context, id uint) error {
	return nil
}

func (r *fakeListenerAccountRepo) SetAccountHealthy(ctx context.Context, id uint, isHealthy bool, lastError string) error {
	return nil
}

func (r *fakeListenerAccountRepo) RecordTaskResult(ctx context.Context, id uint, success bool, lastError string) error {
	r.recordedResults = append(r.recordedResults, listenerRecordedResult{
		accountID: id,
		success:   success,
		lastError: lastError,
	})
	return nil
}

func (r *fakeListenerAccountRepo) GetByGuildAndChannel(ctx context.Context, guildID, channelID string) (*model.Account, error) {
	return nil, nil
}

func contextHasDeadline(ctx context.Context) bool {
	_, ok := ctx.Deadline()
	return ok
}
