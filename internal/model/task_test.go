package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskJSONHidesInternalFields(t *testing.T) {
	accountID := uint(7)
	buttons := `[{"custom_id":"secret-button-id"}]`
	task := Task{
		ID:               123,
		TaskID:           "task-1",
		AccountID:        &accountID,
		Type:             TaskTypeImagine,
		Status:           TaskStatusProcessing,
		DiscordMessageID: "message-secret",
		Buttons:          &buttons,
		CallbackURL:      "https://callback.example.com/hook?token=secret-token",
	}

	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}

	body := string(data)
	for _, forbidden := range []string{
		"secret-token",
		"account_id",
		"callback_url",
		"discord_message_id",
		"buttons",
		"message-secret",
		"secret-button-id",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("task JSON exposed %q: %s", forbidden, body)
		}
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("unmarshal task JSON: %v", err)
	}
	if _, ok := fields["id"]; ok {
		t.Fatalf("task JSON exposed internal id: %s", body)
	}
	for _, expected := range []string{"task_id", "type", "status"} {
		if _, ok := fields[expected]; !ok {
			t.Fatalf("task JSON omitted expected field %q: %s", expected, body)
		}
	}
}

func TestTaskStatusSets(t *testing.T) {
	active := map[TaskStatus]bool{}
	for _, status := range ActiveTaskStatuses() {
		active[status] = true
		if !IsKnownTaskStatus(status) {
			t.Fatalf("active status %s should be known", status)
		}
		if IsTerminalTaskStatus(status) {
			t.Fatalf("active status %s should not be terminal", status)
		}
	}

	for _, status := range []TaskStatus{
		TaskStatusPending,
		TaskStatusSubmitted,
		TaskStatusInQueue,
		TaskStatusProcessing,
	} {
		if !active[status] {
			t.Fatalf("active statuses missing %s", status)
		}
	}

	for _, status := range TerminalTaskStatuses() {
		if !IsKnownTaskStatus(status) {
			t.Fatalf("terminal status %s should be known", status)
		}
		if active[status] {
			t.Fatalf("terminal status %s should not be active", status)
		}
		if !IsTerminalTaskStatus(status) {
			t.Fatalf("terminal status %s was not recognized", status)
		}
	}
	if IsKnownTaskStatus("") || IsKnownTaskStatus("BROKEN") {
		t.Fatal("unknown task status was treated as known")
	}
}

func TestKnownTaskTypes(t *testing.T) {
	for _, taskType := range []TaskType{
		TaskTypeImagine,
		TaskTypeDescribe,
		TaskTypeUpscale,
		TaskTypeZoomOut2x,
		TaskTypeZoomOut1_5x,
		TaskTypeUpscaleSubtle,
		TaskTypeUpscaleCreative,
	} {
		if !IsKnownTaskType(taskType) {
			t.Fatalf("task type %s should be known", taskType)
		}
	}
	if IsKnownTaskType("") || IsKnownTaskType("BROKEN") {
		t.Fatal("unknown task type was treated as known")
	}
}

func TestTaskProgressRange(t *testing.T) {
	for _, progress := range []int{0, 1, 42, 99, 100} {
		if !IsValidTaskProgress(progress) {
			t.Fatalf("progress %d should be valid", progress)
		}
	}

	for _, progress := range []int{-1, 101} {
		if IsValidTaskProgress(progress) {
			t.Fatalf("progress %d should be invalid", progress)
		}
	}
}
