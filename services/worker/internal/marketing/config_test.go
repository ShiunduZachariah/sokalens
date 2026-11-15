package marketing

import (
	"encoding/base64"
	"testing"
)

func base64Decode(value string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(value) }

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestConfigDefaultsToOff(t *testing.T) {
	cfg, err := LoadConfig(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled() || cfg.Location.String() != "Africa/Nairobi" || cfg.PicksPerDay != 3 || cfg.CardBaseURL != "http://web:3000" {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestConfigRequiresSecretsOnlyWhenOn(t *testing.T) {
	base := map[string]string{"MARKETING_MODE": "preview"}
	if _, err := LoadConfig(env(base)); err == nil {
		t.Fatal("preview without a card secret must be refused")
	}
	base["MARKETING_CARD_SECRET"] = "0123456789abcdef0123456789abcdef"
	if _, err := LoadConfig(env(base)); err == nil {
		t.Fatal("preview without a bot token and owner chat must be refused")
	}
	base["MARKETING_TELEGRAM_BOT_TOKEN"] = "token"
	base["MARKETING_TELEGRAM_OWNER_CHAT_ID"] = "1"
	cfg, err := LoadConfig(env(base))
	if err != nil || !cfg.Enabled() || cfg.TelegramChannelEnabled() {
		t.Fatalf("preview config = %+v, %v", cfg, err)
	}
	base["MARKETING_FACEBOOK_PAGE_ID"] = "page"
	if _, err := LoadConfig(env(base)); err == nil {
		t.Fatal("a page id without a token must be refused")
	}
}

func TestConfigRejectsBadValues(t *testing.T) {
	for key, value := range map[string]string{
		"MARKETING_MODE":          "loud",
		"MARKETING_TIMEZONE":      "Mars/Olympus",
		"MARKETING_PICKS_PER_DAY": "9",
		"MARKETING_LEAGUE_IDS":    "12,abc",
	} {
		if _, err := LoadConfig(env(map[string]string{key: value})); err == nil {
			t.Errorf("%s=%s should be refused", key, value)
		}
	}
	cfg, err := LoadConfig(env(map[string]string{"MARKETING_LEAGUE_IDS": " 12, 345 "}))
	if err != nil || len(cfg.LeagueIDs) != 2 || cfg.LeagueIDs[1] != 345 {
		t.Fatalf("league ids = %v, %v", cfg.LeagueIDs, err)
	}
}

func TestLiveModeIsProductionOnly(t *testing.T) {
	values := map[string]string{
		"MARKETING_MODE":                   "live",
		"MARKETING_CARD_SECRET":            "0123456789abcdef0123456789abcdef",
		"MARKETING_TELEGRAM_BOT_TOKEN":     "token",
		"MARKETING_TELEGRAM_OWNER_CHAT_ID": "1",
		"MARKETING_TELEGRAM_CHANNEL":       "@sokalens",
		"APP_ENV":                          "qa",
	}
	cfg, err := LoadConfig(env(values))
	if err != nil || cfg.Mode != ModePreview || !cfg.Downgraded || cfg.TelegramChannelEnabled() {
		t.Fatalf("QA live config = %+v, %v", cfg, err)
	}
	values["APP_ENV"] = "production"
	cfg, err = LoadConfig(env(values))
	if err != nil || cfg.Mode != ModeLive || cfg.Downgraded || !cfg.TelegramChannelEnabled() {
		t.Fatalf("production live config = %+v, %v", cfg, err)
	}
}
