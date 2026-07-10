package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAccountJSONDoesNotExposeUserToken(t *testing.T) {
	account := Account{
		ID:        1,
		GuildID:   "guild-1",
		ChannelID: "channel-1",
		UserToken: "secret-token",
		LastError: `discord API error: user_token="secret-token" callback=https://example.com/hook?token=secret`,
	}

	data, err := json.Marshal(account)
	if err != nil {
		t.Fatalf("marshal account: %v", err)
	}

	body := string(data)
	if strings.Contains(body, "secret-token") {
		t.Fatalf("account JSON exposed user token: %s", body)
	}
	if strings.Contains(body, "user_token") {
		t.Fatalf("account JSON exposed user_token field: %s", body)
	}
	if strings.Contains(body, "last_error") || strings.Contains(body, "token=secret") {
		t.Fatalf("account JSON exposed last_error: %s", body)
	}
	if !strings.Contains(body, "guild_id") || !strings.Contains(body, "channel_id") {
		t.Fatalf("account JSON omitted expected account fields: %s", body)
	}
}
