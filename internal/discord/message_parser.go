package discord

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
)

type MessageParser struct {
	logger          *zap.Logger
	midjourneyBotID string
	progressRegex   *regexp.Regexp
	imageURLRegex   *regexp.Regexp
	urlRegex        *regexp.Regexp
	paramRegex      *regexp.Regexp
}

type ParsedMessage struct {
	MessageID  string
	TaskPrompt string
	Progress   int
	Status     string // "pending", "processing", "completed", "failed"
	ImageURL   string
	Buttons    []string
}

func NewMessageParser(midjourneyBotID string, logger *zap.Logger) *MessageParser {
	return &MessageParser{
		logger:          logger,
		midjourneyBotID: midjourneyBotID,
		// Match progress percentage: (0%), (25%), (50%), (75%), (100%)
		progressRegex: regexp.MustCompile(`\((\d+)%\)`),
		// Match image URL
		imageURLRegex: regexp.MustCompile(`https://cdn\.discordapp\.com/attachments/\S+`),
		// Match http/https URL
		urlRegex: regexp.MustCompile(`<?https?://[^\s>]+>?`),
		// Match Midjourney parameters (e.g. --v 6.1, --ar 16:9)
		paramRegex: regexp.MustCompile(`--[a-z]+\s+[^\s-]+`),
	}
}

// Parse discordgo message
func (p *MessageParser) ParseDiscordMessage(msg *discordgo.Message) *ParsedMessage {
	parsed := &ParsedMessage{
		MessageID: msg.ID,
		Progress:  0,
		Status:    "unknown",
	}

	if msg.Author.ID != p.midjourneyBotID {
		return parsed
	}

	content := msg.Content

	if strings.Contains(content, "**") {
		parts := strings.Split(content, "**")
		if len(parts) >= 2 {
			parsed.TaskPrompt = strings.TrimSpace(parts[1])
		}
	}

	if matches := p.progressRegex.FindStringSubmatch(content); len(matches) > 1 {
		if progress, err := strconv.Atoi(matches[1]); err == nil {
			parsed.Progress = progress

			if progress == 0 {
				parsed.Status = "pending"
			} else if progress == 100 {
				parsed.Status = "completed"
			} else {
				parsed.Status = "processing"
			}
		}
	}

	if strings.Contains(content, "Waiting to start") {
		parsed.Status = "pending"
		parsed.Progress = 0
	}

	if len(msg.Attachments) > 0 {
		for _, attachment := range msg.Attachments {
			if strings.HasPrefix(attachment.ContentType, "image/") {
				parsed.ImageURL = attachment.URL
				if parsed.Progress == 0 {
					parsed.Progress = 100
					parsed.Status = "completed"
				}
				break
			}
		}
	}

	if parsed.ImageURL == "" && len(msg.Embeds) > 0 {
		for _, embed := range msg.Embeds {
			if embed.Image != nil && embed.Image.URL != "" {
				parsed.ImageURL = embed.Image.URL
				break
			}
		}
	}

	if parsed.ImageURL == "" {
		if matches := p.imageURLRegex.FindStringSubmatch(content); len(matches) > 0 {
			parsed.ImageURL = matches[0]
		}
	}

	if len(msg.Components) > 0 {
		parsed.Buttons = p.parseDiscordButtons(msg.Components)
	}

	if strings.Contains(content, "failed") || strings.Contains(content, "error") {
		parsed.Status = "failed"
	}

	return parsed
}

func (p *MessageParser) parseDiscordButtons(components []discordgo.MessageComponent) []string {
	var buttons []string

	for _, comp := range components {
		if actionRow, ok := comp.(*discordgo.ActionsRow); ok {
			for _, component := range actionRow.Components {
				if button, ok := component.(*discordgo.Button); ok {
					if button.CustomID != "" {
						buttons = append(buttons, button.CustomID)
					} else if button.Label != "" {
						buttons = append(buttons, button.Label)
					}
				}
			}
		}
	}

	return buttons
}

func (p *MessageParser) MatchTaskByPrompt(messagePrompt, taskPrompt string) bool {
	if messagePrompt == "" || taskPrompt == "" {
		return false
	}

	msgPrompt := strings.ToLower(strings.TrimSpace(messagePrompt))
	tskPrompt := strings.ToLower(strings.TrimSpace(taskPrompt))

	if msgPrompt == tskPrompt {
		return true
	}

	msgPromptNoURL := p.removeURLsAndParams(msgPrompt)
	tskPromptNoURL := p.removeURLsAndParams(tskPrompt)

	if msgPromptNoURL != "" && tskPromptNoURL != "" {
		if msgPromptNoURL == tskPromptNoURL {
			return true
		}

		if strings.Contains(msgPromptNoURL, tskPromptNoURL) || strings.Contains(tskPromptNoURL, msgPromptNoURL) {
			return true
		}

		maxLen := 100
		if len(msgPromptNoURL) > maxLen {
			msgPromptNoURL = msgPromptNoURL[:maxLen]
		}
		if len(tskPromptNoURL) > maxLen {
			tskPromptNoURL = tskPromptNoURL[:maxLen]
		}

		if strings.Contains(msgPromptNoURL, tskPromptNoURL) || strings.Contains(tskPromptNoURL, msgPromptNoURL) {
			return true
		}
	}

	if strings.Contains(msgPrompt, tskPrompt) {
		return true
	}

	return false
}

func (p *MessageParser) removeURLsAndParams(text string) string {
	result := p.urlRegex.ReplaceAllString(text, "")
	result = p.paramRegex.ReplaceAllString(result, "")
	result = strings.Join(strings.Fields(result), " ")
	return strings.TrimSpace(result)
}
