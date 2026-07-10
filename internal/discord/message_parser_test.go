package discord

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/trae/midjourney-api/internal/model"
	"go.uber.org/zap"
)

func TestParseDiscordMessageHandlesPartialUpdateWithoutAuthor(t *testing.T) {
	parser := NewMessageParser("midjourney-bot", zap.NewNop())

	parsed := parser.ParseDiscordMessage(&discordgo.Message{
		ID:      "message-1",
		Content: "**a quiet harbor** (42%)",
		Embeds:  []*discordgo.MessageEmbed{nil},
	})

	if parsed == nil {
		t.Fatalf("expected parsed message")
	}
	if parsed.Status != "processing" {
		t.Fatalf("status = %q, want processing", parsed.Status)
	}
	if parsed.Progress != 42 {
		t.Fatalf("progress = %d, want 42", parsed.Progress)
	}
}

func TestParseDiscordMessageReturnsNilForNilMessage(t *testing.T) {
	parser := NewMessageParser("midjourney-bot", zap.NewNop())

	if parsed := parser.ParseDiscordMessage(nil); parsed != nil {
		t.Fatalf("expected nil parsed message, got %#v", parsed)
	}
}

func TestParseDiscordMessageAllowsNilLogger(t *testing.T) {
	parser := NewMessageParser("midjourney-bot", nil)

	parsed := parser.ParseDiscordMessage(&discordgo.Message{
		ID: "message-describe",
		Embeds: []*discordgo.MessageEmbed{
			{
				Description: "1\ufe0f\u20e3 first prompt\n\n2\ufe0f\u20e3 second prompt",
			},
		},
	})

	if parsed == nil {
		t.Fatal("expected parsed message")
	}
	if !parsed.IsDescribeResult {
		t.Fatalf("IsDescribeResult = false, want true")
	}
	if parsed.Descriptions != "first prompt" {
		t.Fatalf("description = %q, want first prompt", parsed.Descriptions)
	}
}

func TestParseDiscordMessageDoesNotTreatPromptTextAsFailure(t *testing.T) {
	parser := NewMessageParser("midjourney-bot", zap.NewNop())

	parsed := parser.ParseDiscordMessage(&discordgo.Message{
		ID:      "message-error-prompt",
		Content: "**an error themed poster** (42%)",
	})

	if parsed == nil {
		t.Fatal("expected parsed message")
	}
	if parsed.Status != "processing" {
		t.Fatalf("status = %q, want processing", parsed.Status)
	}
	if parsed.Progress != 42 {
		t.Fatalf("progress = %d, want 42", parsed.Progress)
	}
}

func TestParseDiscordMessageDetectsFailureCaseInsensitively(t *testing.T) {
	parser := NewMessageParser("midjourney-bot", zap.NewNop())

	parsed := parser.ParseDiscordMessage(&discordgo.Message{
		ID:      "message-failed",
		Content: "Failed to process your command",
	})

	if parsed == nil {
		t.Fatal("expected parsed message")
	}
	if parsed.Status != "failed" {
		t.Fatalf("status = %q, want failed", parsed.Status)
	}
}

func TestParseDiscordMessageCompletesImageURLWithoutProgress(t *testing.T) {
	parser := NewMessageParser("midjourney-bot", zap.NewNop())

	tests := []struct {
		name string
		msg  *discordgo.Message
	}{
		{
			name: "embed image",
			msg: &discordgo.Message{
				ID: "message-embed",
				Embeds: []*discordgo.MessageEmbed{
					{Image: &discordgo.MessageEmbedImage{URL: "https://cdn.discordapp.com/attachments/1/2/image.png"}},
				},
			},
		},
		{
			name: "content image",
			msg: &discordgo.Message{
				ID:      "message-content",
				Content: "https://cdn.discordapp.com/attachments/1/2/image.png",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed := parser.ParseDiscordMessage(tt.msg)
			if parsed == nil {
				t.Fatal("expected parsed message")
			}
			if parsed.Status != "completed" {
				t.Fatalf("status = %q, want completed", parsed.Status)
			}
			if parsed.Progress != 100 {
				t.Fatalf("progress = %d, want 100", parsed.Progress)
			}
			if parsed.ImageURL == "" {
				t.Fatal("image URL was empty")
			}
		})
	}
}

func TestParseDiscordMessageIgnoresOutOfRangeProgress(t *testing.T) {
	parser := NewMessageParser("midjourney-bot", zap.NewNop())

	parsed := parser.ParseDiscordMessage(&discordgo.Message{
		ID:      "message-bad-progress",
		Content: "**a quiet harbor** (101%)",
	})

	if parsed == nil {
		t.Fatal("expected parsed message")
	}
	if parsed.Status != "unknown" {
		t.Fatalf("status = %q, want unknown", parsed.Status)
	}
	if parsed.Progress != 0 {
		t.Fatalf("progress = %d, want 0", parsed.Progress)
	}
}

func TestIsTerminalTaskStatus(t *testing.T) {
	terminalStatuses := []model.TaskStatus{
		model.TaskStatusSuccess,
		model.TaskStatusFailed,
		model.TaskStatusTimeout,
	}
	for _, status := range terminalStatuses {
		if !model.IsTerminalTaskStatus(status) {
			t.Fatalf("expected %s to be terminal", status)
		}
	}

	nonTerminalStatuses := []model.TaskStatus{
		model.TaskStatusPending,
		model.TaskStatusSubmitted,
		model.TaskStatusInQueue,
		model.TaskStatusProcessing,
	}
	for _, status := range nonTerminalStatuses {
		if model.IsTerminalTaskStatus(status) {
			t.Fatalf("expected %s to be non-terminal", status)
		}
	}
}
