package discord

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/trae/midjourney-api/pkg/constants"
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
	MessageID        string
	TaskPrompt       string
	Progress         int
	Status           string // "pending", "processing", "completed", "failed"
	ImageURL         string
	Buttons          []string
	IsDescribeResult bool   // true if this is a describe result message
	Descriptions     string // describe result text
}

func NewMessageParser(midjourneyBotID string, logger *zap.Logger) *MessageParser {
	if logger == nil {
		logger = zap.NewNop()
	}

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
	if msg == nil {
		return nil
	}

	parsed := &ParsedMessage{
		MessageID: msg.ID,
		Progress:  0,
		Status:    "unknown",
	}

	if msg.Author != nil && msg.Author.ID != p.midjourneyBotID {
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
			if !validParsedProgress(progress) {
				p.logger.Warn("Ignoring out-of-range Midjourney progress",
					zap.String("message_id", parsed.MessageID),
					zap.Int("progress", progress))
			} else {
				parsed.Progress = progress

				if progress == constants.MinTaskProgress {
					parsed.Status = "pending"
				} else if progress == constants.MaxTaskProgress {
					parsed.Status = "completed"
				} else {
					parsed.Status = "processing"
				}
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
			if embed == nil {
				continue
			}
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
	if parsed.ImageURL != "" && parsed.Progress == 0 {
		parsed.Progress = 100
		parsed.Status = "completed"
	}

	if len(msg.Components) > 0 {
		parsed.Buttons = p.parseDiscordButtons(msg.Components)
	}

	if parsed.Status == "unknown" && isFailureContent(content) {
		parsed.Status = "failed"
	}

	// Detect describe result: Midjourney returns descriptions in embed.Description
	describeText := ""
	for _, embed := range msg.Embeds {
		if embed == nil {
			continue
		}
		if strings.Contains(embed.Description, "1\ufe0f\u20e3") && strings.Contains(embed.Description, "2\ufe0f\u20e3") {
			describeText = embed.Description
			break
		}
	}
	// Fallback: also check msg.Content
	if describeText == "" && strings.Contains(content, "1\ufe0f\u20e3") && strings.Contains(content, "2\ufe0f\u20e3") {
		describeText = content
	}
	if describeText != "" {
		parsed.IsDescribeResult = true
		parsed.Descriptions = extractFirstDescription(describeText)
		parsed.Status = "completed"
		parsed.Progress = 100
		p.logger.Info("[Describe result detected]",
			zap.String("message_id", parsed.MessageID),
			zap.Int("desc_len", len(parsed.Descriptions)),
		)
	}

	return parsed
}

func validParsedProgress(progress int) bool {
	return progress >= constants.MinTaskProgress && progress <= constants.MaxTaskProgress
}

func isFailureContent(content string) bool {
	content = strings.ToLower(content)
	return strings.Contains(content, "failed") || strings.Contains(content, "error")
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

// extractFirstDescription extracts only the first numbered description from Midjourney describe results.
// Input format: "1️⃣ text1\n\n2️⃣ text2\n\n3️⃣ text3\n\n4️⃣ text4"
func extractFirstDescription(text string) string {
	marker1 := "1\ufe0f\u20e3"
	marker2 := "2\ufe0f\u20e3"

	start := strings.Index(text, marker1)
	if start == -1 {
		return strings.TrimSpace(text)
	}
	start += len(marker1)

	end := strings.Index(text[start:], marker2)
	var first string
	if end == -1 {
		first = text[start:]
	} else {
		first = text[start : start+end]
	}
	return strings.TrimSpace(first)
}
