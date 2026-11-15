// Package marketing runs the automated distribution routine: the daily picks
// card, the honest results card, live goal updates, the weekly record, story
// packs and short videos for the channels that have no posting API, and the
// Sunday numbers report.
//
// It reads only our own tables and calls no football provider. Everything it
// posts is something the platform already published: a pick it shows is a
// published prediction, and a result it shows is graded from the same stored
// rows the public record uses.
//
// Three modes, so the routine can be watched before it speaks in public:
//
//   - off      nothing is scheduled (the default)
//   - preview  every post goes only to the owner's Telegram chat, labelled
//   - live     posts go to the public channels, and the owner gets the packs
//     for WhatsApp, TikTok and Status that have to be posted by hand
package marketing

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	// The worker image is Alpine without tzdata. Embedding the database is
	// what makes Africa/Nairobi resolve in production rather than failing at
	// startup, or worse, quietly falling back to UTC and posting at 05:00.
	_ "time/tzdata"
)

// Mode is how far the routine is allowed to speak.
type Mode string

const (
	ModeOff     Mode = "off"
	ModePreview Mode = "preview"
	ModeLive    Mode = "live"
)

// Config is read from the environment once at startup.
type Config struct {
	Mode Mode

	// CardSecret signs card tokens. The web card route refuses any token it
	// cannot verify, so a stranger cannot put a promise of certain winnings
	// on our template by editing a URL.
	CardSecret string
	// CardBaseURL is where the worker fetches rendered cards from. Inside
	// Compose that is the web container directly, so the fetch never crosses
	// Cloudflare and cannot be challenged by it.
	CardBaseURL string
	// SiteURL is the public origin used in every link a reader sees.
	SiteURL string

	Location *time.Location
	// PicksHour is the local hour after which the morning card goes out.
	PicksHour int
	// PicksPerDay is how many picks the morning card carries.
	PicksPerDay int
	// LeagueIDs are canonical leagues to include regardless of priority; this
	// is how a league the global ranking does not score (the Kenyan Premier
	// League, for one) gets into the morning card.
	LeagueIDs []int64
	// MinLeaguePriority admits any league at or above this leagues.priority_score.
	MinLeaguePriority int

	LiveGoals bool
	// LiveLeagueIDs and LiveMinLeaguePriority choose the big matches whose
	// goals are posted, on top of every match in the day's picks.
	LiveLeagueIDs         []int64
	LiveMinLeaguePriority int
	LiveMaxPerDay         int

	TelegramBotToken    string
	TelegramChannel     string
	TelegramOwnerChatID string

	FacebookPageID       string
	FacebookPageToken    string
	FacebookGraphVersion string

	VideoEnabled bool
	FFmpegPath   string

	// Downgraded is set when live was asked for outside production and the
	// routine was held to preview instead.
	Downgraded bool
}

// Enabled reports whether anything should be scheduled at all.
func (c Config) Enabled() bool { return c.Mode == ModePreview || c.Mode == ModeLive }

// TelegramChannelEnabled reports whether public Telegram posts can be made.
func (c Config) TelegramChannelEnabled() bool {
	return c.Mode == ModeLive && c.TelegramChannel != ""
}

// FacebookEnabled reports whether public Facebook Page posts can be made.
func (c Config) FacebookEnabled() bool {
	return c.Mode == ModeLive && c.FacebookPageID != "" && c.FacebookPageToken != ""
}

// LoadConfig reads the marketing settings. getenv is os.Getenv in production.
func LoadConfig(getenv func(string) string) (Config, error) {
	get := func(key, fallback string) string {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			return value
		}
		return fallback
	}
	cfg := Config{
		Mode:                 Mode(strings.ToLower(get("MARKETING_MODE", string(ModeOff)))),
		CardSecret:           get("MARKETING_CARD_SECRET", ""),
		CardBaseURL:          strings.TrimRight(get("MARKETING_CARD_BASE_URL", "http://web:3000"), "/"),
		SiteURL:              strings.TrimRight(get("MARKETING_SITE_URL", "https://sokalens.com"), "/"),
		TelegramBotToken:     get("MARKETING_TELEGRAM_BOT_TOKEN", ""),
		TelegramChannel:      get("MARKETING_TELEGRAM_CHANNEL", ""),
		TelegramOwnerChatID:  get("MARKETING_TELEGRAM_OWNER_CHAT_ID", ""),
		FacebookPageID:       get("MARKETING_FACEBOOK_PAGE_ID", ""),
		FacebookPageToken:    get("MARKETING_FACEBOOK_PAGE_TOKEN", ""),
		FacebookGraphVersion: get("MARKETING_FACEBOOK_GRAPH_VERSION", "v23.0"),
		FFmpegPath:           get("MARKETING_FFMPEG_PATH", "ffmpeg"),
		LiveGoals:            parseBool(get("MARKETING_LIVE_GOALS", "true")),
		VideoEnabled:         parseBool(get("MARKETING_VIDEO_ENABLED", "true")),
	}
	switch cfg.Mode {
	case ModeOff, ModePreview, ModeLive:
	default:
		return Config{}, fmt.Errorf("MARKETING_MODE must be off, preview or live, got %q", cfg.Mode)
	}

	zone := get("MARKETING_TIMEZONE", "Africa/Nairobi")
	location, err := time.LoadLocation(zone)
	if err != nil {
		return Config{}, fmt.Errorf("MARKETING_TIMEZONE %q: %w", zone, err)
	}
	cfg.Location = location

	if cfg.PicksHour, err = boundedInt(get("MARKETING_PICKS_HOUR", "8"), "MARKETING_PICKS_HOUR", 0, 20); err != nil {
		return Config{}, err
	}
	if cfg.PicksPerDay, err = boundedInt(get("MARKETING_PICKS_PER_DAY", "3"), "MARKETING_PICKS_PER_DAY", 1, 5); err != nil {
		return Config{}, err
	}
	if cfg.MinLeaguePriority, err = boundedInt(get("MARKETING_MIN_LEAGUE_PRIORITY", "60"), "MARKETING_MIN_LEAGUE_PRIORITY", 0, 1000); err != nil {
		return Config{}, err
	}
	if cfg.LiveMinLeaguePriority, err = boundedInt(get("MARKETING_LIVE_MIN_LEAGUE_PRIORITY", "84"), "MARKETING_LIVE_MIN_LEAGUE_PRIORITY", 0, 1000); err != nil {
		return Config{}, err
	}
	if cfg.LiveMaxPerDay, err = boundedInt(get("MARKETING_LIVE_MAX_PER_DAY", "15"), "MARKETING_LIVE_MAX_PER_DAY", 0, 100); err != nil {
		return Config{}, err
	}
	if cfg.LeagueIDs, err = idList(get("MARKETING_LEAGUE_IDS", ""), "MARKETING_LEAGUE_IDS"); err != nil {
		return Config{}, err
	}
	if cfg.LiveLeagueIDs, err = idList(get("MARKETING_LIVE_LEAGUE_IDS", ""), "MARKETING_LIVE_LEAGUE_IDS"); err != nil {
		return Config{}, err
	}

	// Only production speaks in public. QA runs the same code against its own
	// database, and a bot token copied into .env.qa would otherwise post QA's
	// picks to the real channel. Held to preview, it can still be tested.
	if cfg.Mode == ModeLive && !strings.EqualFold(get("APP_ENV", ""), "production") {
		cfg.Mode = ModePreview
		cfg.Downgraded = true
	}

	if !cfg.Enabled() {
		return cfg, nil
	}
	// Preview and live both need a way to sign cards and somebody to show
	// them to. The owner chat is required in live mode as well: it is where
	// the WhatsApp, TikTok and Status packs go, and where failures are reported.
	if len(cfg.CardSecret) < 32 {
		return Config{}, errors.New("MARKETING_CARD_SECRET must be at least 32 characters when MARKETING_MODE is not off")
	}
	if cfg.TelegramBotToken == "" || cfg.TelegramOwnerChatID == "" {
		return Config{}, errors.New("MARKETING_TELEGRAM_BOT_TOKEN and MARKETING_TELEGRAM_OWNER_CHAT_ID are required when MARKETING_MODE is not off")
	}
	if (cfg.FacebookPageID == "") != (cfg.FacebookPageToken == "") {
		return Config{}, errors.New("MARKETING_FACEBOOK_PAGE_ID and MARKETING_FACEBOOK_PAGE_TOKEN must be set together")
	}
	return cfg, nil
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func boundedInt(value, name string, minimum, maximum int) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	return parsed, nil
}

func idList(value, name string) ([]int64, error) {
	var ids []int64
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%s must be a comma-separated list of positive ids, got %q", name, part)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
