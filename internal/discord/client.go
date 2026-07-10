package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/internal/safehttp"
	"github.com/trae/midjourney-api/pkg/constants"
	"github.com/trae/midjourney-api/pkg/redact"
)

type Client struct {
	cfg        *config.DiscordConfig
	httpClient *http.Client
}

const maxDiscordErrorBodyBytes = 1024

var defaultDiscordHTTPClient = newDiscordHTTPClient()

func newDiscordHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil

	return &http.Client{
		Timeout:   constants.DefaultHTTPTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func NewClient(cfg *config.DiscordConfig) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: defaultDiscordHTTPClient,
	}
}

type ImagineRequest struct {
	Prompt    string
	GuildID   string
	ChannelID string
	UserToken string
}

type InteractionPayload struct {
	Type          int             `json:"type"`
	ApplicationID string          `json:"application_id"`
	GuildID       string          `json:"guild_id"`
	ChannelID     string          `json:"channel_id"`
	SessionID     string          `json:"session_id"`
	Data          InteractionData `json:"data"`
	Nonce         string          `json:"nonce"`
	Analytics     string          `json:"analytics_location"`
}

type InteractionData struct {
	Version            string              `json:"version"`
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Type               int                 `json:"type"`
	Options            []InteractionOption `json:"options"`
	ApplicationCommand ApplicationCommand  `json:"application_command"`
	Attachments        []interface{}       `json:"attachments"`
}

type InteractionOption struct {
	Type  int    `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ApplicationCommand struct {
	ID                   string          `json:"id"`
	Type                 int             `json:"type"`
	ApplicationID        string          `json:"application_id"`
	Version              string          `json:"version"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Options              []CommandOption `json:"options"`
	DMPermission         bool            `json:"dm_permission"`
	Contexts             []int           `json:"contexts"`
	IntegrationTypes     []int           `json:"integration_types"`
	GlobalPopularityRank int             `json:"global_popularity_rank"`
	DescriptionLocalized string          `json:"description_localized"`
	NameLocalized        string          `json:"name_localized"`
}

type CommandOption struct {
	Type                 int    `json:"type"`
	Name                 string `json:"name"`
	Description          string `json:"description"`
	Required             bool   `json:"required"`
	DescriptionLocalized string `json:"description_localized"`
	NameLocalized        string `json:"name_localized"`
}

func (c *Client) doDiscordRequest(ctx context.Context, payload interface{}, userToken, guildID, channelID string) error {
	apiBaseURL, userToken, guildID, channelID, err := c.validateDiscordRequest(userToken, guildID, channelID)
	if err != nil {
		return err
	}
	if payload == nil {
		return fmt.Errorf("discord payload is required")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	url := discordInteractionsURL(apiBaseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", redact.Error(err))
	}

	httpReq.Header.Set("Authorization", userToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", constants.DefaultUserAgent)
	httpReq.Header.Set("Origin", constants.DiscordOrigin)
	httpReq.Header.Set("Referer", fmt.Sprintf("%s/channels/%s/%s", constants.DiscordOrigin, guildID, channelID))

	resp, err := c.httpClientOrDefault().Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", redact.Error(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("discord API error: status=%d, body=%q",
			resp.StatusCode, readDiscordErrorBody(resp.Body))
	}

	return nil
}

func (c *Client) httpClientOrDefault() *http.Client {
	if c != nil && c.httpClient != nil {
		return c.httpClient
	}
	return defaultDiscordHTTPClient
}

func (c *Client) validateDiscordRequest(userToken, guildID, channelID string) (apiBaseURL, trimmedToken, trimmedGuildID, trimmedChannelID string, err error) {
	cfg, err := c.requireConfig()
	if err != nil {
		return "", "", "", "", err
	}
	apiBaseURL, err = requireDiscordField("discord api_base_url", cfg.APIBaseURL)
	if err != nil {
		return "", "", "", "", err
	}
	if err := validateDiscordAPIBaseURL("discord api_base_url", apiBaseURL); err != nil {
		return "", "", "", "", err
	}
	trimmedToken, err = requireDiscordField("user_token", userToken)
	if err != nil {
		return "", "", "", "", err
	}
	trimmedGuildID, err = requireDiscordField("guild_id", guildID)
	if err != nil {
		return "", "", "", "", err
	}
	trimmedChannelID, err = requireDiscordField("channel_id", channelID)
	if err != nil {
		return "", "", "", "", err
	}
	return apiBaseURL, trimmedToken, trimmedGuildID, trimmedChannelID, nil
}

func (c *Client) requireConfig() (*config.DiscordConfig, error) {
	if c == nil {
		return nil, fmt.Errorf("discord client is nil")
	}
	if c.cfg == nil {
		return nil, fmt.Errorf("discord config is nil")
	}
	cfg := *c.cfg
	normalizeDiscordConfig(&cfg)
	return &cfg, nil
}

func normalizeDiscordConfig(cfg *config.DiscordConfig) {
	if cfg == nil {
		return
	}
	cfg.ApplicationID = strings.TrimSpace(cfg.ApplicationID)
	cfg.ImagineCommandID = strings.TrimSpace(cfg.ImagineCommandID)
	cfg.ImagineCommandVersion = strings.TrimSpace(cfg.ImagineCommandVersion)
	cfg.DescribeCommandID = strings.TrimSpace(cfg.DescribeCommandID)
	cfg.DescribeCommandVersion = strings.TrimSpace(cfg.DescribeCommandVersion)
	cfg.APIBaseURL = strings.TrimSpace(cfg.APIBaseURL)
}

func requireDiscordField(name, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return trimmed, nil
}

func validateDiscordHTTPURL(name, value string) error {
	return safehttp.ValidateHTTPURL(value, name)
}

func validateDiscordAPIBaseURL(name, value string) error {
	if err := validateDiscordHTTPURL(name, value); err != nil {
		return err
	}
	parsed, _ := url.Parse(strings.TrimSpace(value))
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain query or fragment", name)
	}
	return nil
}

func discordInteractionsURL(apiBaseURL string) string {
	return strings.TrimRight(strings.TrimSpace(apiBaseURL), "/") + "/interactions"
}

func readDiscordErrorBody(body io.Reader) string {
	if body == nil {
		return ""
	}

	data, err := io.ReadAll(io.LimitReader(body, maxDiscordErrorBodyBytes+1))
	if err != nil {
		return "failed to read response body"
	}
	truncated := len(data) > maxDiscordErrorBodyBytes
	if truncated {
		data = data[:maxDiscordErrorBodyBytes]
	}
	message := string(bytes.ToValidUTF8(data, nil))
	if truncated {
		message += "...<truncated>"
	}
	return redact.Text(message)
}

func (c *Client) Imagine(ctx context.Context, req *ImagineRequest) error {
	cfg, prompt, userToken, guildID, channelID, err := c.validateImagineRequest(req)
	if err != nil {
		return err
	}

	payload := InteractionPayload{
		Type:          2,
		ApplicationID: cfg.ApplicationID,
		GuildID:       guildID,
		ChannelID:     channelID,
		SessionID:     generateUUID(),
		Data: InteractionData{
			Version: cfg.ImagineCommandVersion,
			ID:      cfg.ImagineCommandID,
			Name:    "imagine",
			Type:    1,
			Options: []InteractionOption{
				{
					Type:  3,
					Name:  "prompt",
					Value: prompt,
				},
			},
			ApplicationCommand: ApplicationCommand{
				ID:            cfg.ImagineCommandID,
				Type:          1,
				ApplicationID: cfg.ApplicationID,
				Version:       cfg.ImagineCommandVersion,
				Name:          "imagine",
				Description:   "Create images with Midjourney",
				Options: []CommandOption{
					{
						Type:                 3,
						Name:                 "prompt",
						Description:          "The prompt to imagine",
						Required:             true,
						DescriptionLocalized: "The prompt to imagine",
						NameLocalized:        "prompt",
					},
				},
				DMPermission:         true,
				Contexts:             []int{0, 1, 2},
				IntegrationTypes:     []int{0, 1},
				GlobalPopularityRank: 1,
				DescriptionLocalized: "Create images with Midjourney",
				NameLocalized:        "imagine",
			},
			Attachments: []interface{}{},
		},
		Nonce:     generateUUID(),
		Analytics: "slash_ui",
	}

	return c.doDiscordRequest(ctx, payload, userToken, guildID, channelID)
}

func (c *Client) validateImagineRequest(req *ImagineRequest) (*config.DiscordConfig, string, string, string, string, error) {
	if req == nil {
		return nil, "", "", "", "", fmt.Errorf("imagine request is required")
	}
	cfg, err := c.requireConfig()
	if err != nil {
		return nil, "", "", "", "", err
	}
	if _, err := requireDiscordField("discord application_id", cfg.ApplicationID); err != nil {
		return nil, "", "", "", "", err
	}
	if _, err := requireDiscordField("discord imagine_command_id", cfg.ImagineCommandID); err != nil {
		return nil, "", "", "", "", err
	}
	if _, err := requireDiscordField("discord imagine_command_version", cfg.ImagineCommandVersion); err != nil {
		return nil, "", "", "", "", err
	}
	prompt, err := requireDiscordField("prompt", req.Prompt)
	if err != nil {
		return nil, "", "", "", "", err
	}
	_, userToken, guildID, channelID, err := c.validateDiscordRequest(req.UserToken, req.GuildID, req.ChannelID)
	if err != nil {
		return nil, "", "", "", "", err
	}
	return cfg, prompt, userToken, guildID, channelID, nil
}

func generateUUID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

type DescribeRequest struct {
	ImageURL  string
	GuildID   string
	ChannelID string
	UserToken string
}

func (c *Client) Describe(ctx context.Context, req *DescribeRequest) error {
	cfg, imageURL, userToken, guildID, channelID, err := c.validateDescribeRequest(req)
	if err != nil {
		return err
	}

	payload := InteractionPayload{
		Type:          2,
		ApplicationID: cfg.ApplicationID,
		GuildID:       guildID,
		ChannelID:     channelID,
		SessionID:     generateUUID(),
		Data: InteractionData{
			Version: cfg.DescribeCommandVersion,
			ID:      cfg.DescribeCommandID,
			Name:    "describe",
			Type:    1,
			Options: []InteractionOption{
				{
					Type:  3,
					Name:  "link",
					Value: imageURL,
				},
			},
			ApplicationCommand: ApplicationCommand{
				ID:            cfg.DescribeCommandID,
				Type:          1,
				ApplicationID: cfg.ApplicationID,
				Version:       cfg.DescribeCommandVersion,
				Name:          "describe",
				Description:   "Describes your image as a prompt.",
				Options: []CommandOption{
					{
						Type:                 11,
						Name:                 "image",
						Description:          "The image to describe",
						Required:             false,
						DescriptionLocalized: "The image to describe",
						NameLocalized:        "image",
					},
					{
						Type:                 3,
						Name:                 "link",
						Description:          "\u2026",
						Required:             false,
						DescriptionLocalized: "\u2026",
						NameLocalized:        "link",
					},
				},
				DMPermission:         true,
				Contexts:             []int{0, 1, 2},
				IntegrationTypes:     []int{0, 1},
				GlobalPopularityRank: 2,
				DescriptionLocalized: "Describes your image as a prompt.",
				NameLocalized:        "describe",
			},
			Attachments: []interface{}{},
		},
		Nonce:     generateUUID(),
		Analytics: "slash_ui",
	}

	return c.doDiscordRequest(ctx, payload, userToken, guildID, channelID)
}

func (c *Client) validateDescribeRequest(req *DescribeRequest) (*config.DiscordConfig, string, string, string, string, error) {
	if req == nil {
		return nil, "", "", "", "", fmt.Errorf("describe request is required")
	}
	cfg, err := c.requireConfig()
	if err != nil {
		return nil, "", "", "", "", err
	}
	if _, err := requireDiscordField("discord application_id", cfg.ApplicationID); err != nil {
		return nil, "", "", "", "", err
	}
	if _, err := requireDiscordField("discord describe_command_id", cfg.DescribeCommandID); err != nil {
		return nil, "", "", "", "", err
	}
	if _, err := requireDiscordField("discord describe_command_version", cfg.DescribeCommandVersion); err != nil {
		return nil, "", "", "", "", err
	}
	imageURL, err := requireDiscordField("image_url", req.ImageURL)
	if err != nil {
		return nil, "", "", "", "", err
	}
	if err := validateDiscordHTTPURL("image_url", imageURL); err != nil {
		return nil, "", "", "", "", err
	}
	_, userToken, guildID, channelID, err := c.validateDiscordRequest(req.UserToken, req.GuildID, req.ChannelID)
	if err != nil {
		return nil, "", "", "", "", err
	}
	return cfg, imageURL, userToken, guildID, channelID, nil
}

type ButtonActionRequest struct {
	CustomID  string // Example: "MJ::JOB::upsample::1::bfcd9434-6f90-47a0-b604-5c2208151a3c"
	MessageID string
	GuildID   string
	ChannelID string
	UserToken string
}

type ButtonInteractionPayload struct {
	Type          int                   `json:"type"`
	ApplicationID string                `json:"application_id"`
	GuildID       string                `json:"guild_id"`
	ChannelID     string                `json:"channel_id"`
	SessionID     string                `json:"session_id"`
	Data          ButtonInteractionData `json:"data"`
	Nonce         string                `json:"nonce"`
	MessageFlags  int                   `json:"message_flags,omitempty"`
	MessageID     string                `json:"message_id"`
}

type ButtonInteractionData struct {
	ComponentType int    `json:"component_type"`
	CustomID      string `json:"custom_id"`
}

func (c *Client) PerformButtonAction(ctx context.Context, req *ButtonActionRequest) error {
	cfg, customID, messageID, userToken, guildID, channelID, err := c.validateButtonActionRequest(req)
	if err != nil {
		return err
	}

	payload := ButtonInteractionPayload{
		Type:          3, // 3 represents a button interaction
		ApplicationID: cfg.ApplicationID,
		GuildID:       guildID,
		ChannelID:     channelID,
		SessionID:     generateUUID(),
		Data: ButtonInteractionData{
			ComponentType: 2, // 2 represents a button component
			CustomID:      customID,
		},
		Nonce:     generateUUID(),
		MessageID: messageID,
	}

	return c.doDiscordRequest(ctx, payload, userToken, guildID, channelID)
}

func (c *Client) validateButtonActionRequest(req *ButtonActionRequest) (*config.DiscordConfig, string, string, string, string, string, error) {
	if req == nil {
		return nil, "", "", "", "", "", fmt.Errorf("button action request is required")
	}
	cfg, err := c.requireConfig()
	if err != nil {
		return nil, "", "", "", "", "", err
	}
	if _, err := requireDiscordField("discord application_id", cfg.ApplicationID); err != nil {
		return nil, "", "", "", "", "", err
	}
	customID, err := requireDiscordField("custom_id", req.CustomID)
	if err != nil {
		return nil, "", "", "", "", "", err
	}
	messageID, err := requireDiscordField("message_id", req.MessageID)
	if err != nil {
		return nil, "", "", "", "", "", err
	}
	_, userToken, guildID, channelID, err := c.validateDiscordRequest(req.UserToken, req.GuildID, req.ChannelID)
	if err != nil {
		return nil, "", "", "", "", "", err
	}
	return cfg, customID, messageID, userToken, guildID, channelID, nil
}
