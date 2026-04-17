package discord

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/trae/midjourney-api/internal/config"
	"github.com/trae/midjourney-api/pkg/constants"
)

type Client struct {
	cfg        *config.DiscordConfig
	httpClient *http.Client
}

func NewClient(cfg *config.DiscordConfig) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{},
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

func (c *Client) doDiscordRequest(payload interface{}, userToken, guildID, channelID string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s/interactions", c.cfg.APIBaseURL)
	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", userToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", constants.DefaultUserAgent)
	httpReq.Header.Set("Origin", constants.DiscordOrigin)
	httpReq.Header.Set("Referer", fmt.Sprintf("%s/channels/%s/%s", constants.DiscordOrigin, guildID, channelID))

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("discord API error: status=%d, body=%s, guild_id=%s, channel_id=%s",
			resp.StatusCode, string(respBody), guildID, channelID)
	}

	return nil
}

func (c *Client) Imagine(req *ImagineRequest) error {
	payload := InteractionPayload{
		Type:          2,
		ApplicationID: c.cfg.ApplicationID,
		GuildID:       req.GuildID,
		ChannelID:     req.ChannelID,
		SessionID:     generateUUID(),
		Data: InteractionData{
			Version: c.cfg.ImagineCommandVersion,
			ID:      c.cfg.ImagineCommandID,
			Name:    "imagine",
			Type:    1,
			Options: []InteractionOption{
				{
					Type:  3,
					Name:  "prompt",
					Value: req.Prompt,
				},
			},
			ApplicationCommand: ApplicationCommand{
				ID:            c.cfg.ImagineCommandID,
				Type:          1,
				ApplicationID: c.cfg.ApplicationID,
				Version:       c.cfg.ImagineCommandVersion,
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

	return c.doDiscordRequest(payload, req.UserToken, req.GuildID, req.ChannelID)
}

func generateUUID() string {
	return uuid.New().String()[:32]
}

type DescribeRequest struct {
	ImageURL  string
	GuildID   string
	ChannelID string
	UserToken string
}

func (c *Client) Describe(req *DescribeRequest) error {
	payload := InteractionPayload{
		Type:          2,
		ApplicationID: c.cfg.ApplicationID,
		GuildID:       req.GuildID,
		ChannelID:     req.ChannelID,
		SessionID:     generateUUID(),
		Data: InteractionData{
			Version: c.cfg.DescribeCommandVersion,
			ID:      c.cfg.DescribeCommandID,
			Name:    "describe",
			Type:    1,
			Options: []InteractionOption{
				{
					Type:  3,
					Name:  "link",
					Value: req.ImageURL,
				},
			},
			ApplicationCommand: ApplicationCommand{
				ID:            c.cfg.DescribeCommandID,
				Type:          1,
				ApplicationID: c.cfg.ApplicationID,
				Version:       c.cfg.DescribeCommandVersion,
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

	return c.doDiscordRequest(payload, req.UserToken, req.GuildID, req.ChannelID)
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

func (c *Client) PerformButtonAction(req *ButtonActionRequest) error {
	payload := ButtonInteractionPayload{
		Type:          3, // 3 represents a button interaction
		ApplicationID: c.cfg.ApplicationID,
		GuildID:       req.GuildID,
		ChannelID:     req.ChannelID,
		SessionID:     generateUUID(),
		Data: ButtonInteractionData{
			ComponentType: 2, // 2 represents a button component
			CustomID:      req.CustomID,
		},
		Nonce:     generateUUID(),
		MessageID: req.MessageID,
	}

	return c.doDiscordRequest(payload, req.UserToken, req.GuildID, req.ChannelID)
}
