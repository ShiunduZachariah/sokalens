package marketing

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

// Queue and job keys. The routine has its own queue so a slow card render or
// a channel timeout never holds a slot that live polling might want.
const (
	Queue      = "marketing"
	TickJobKey = "marketing.tick"
	LiveJobKey = "marketing.live"
)

// TickHandler runs the clock-driven posts.
type TickHandler struct{ Service *Service }

// Run implements ingestion.JobHandler.
func (h TickHandler) Run(ctx context.Context, _ jobs.Job) error { return h.Service.Tick(ctx) }

// LiveHandler runs goal updates.
type LiveHandler struct{ Service *Service }

// Run implements ingestion.JobHandler.
func (h LiveHandler) Run(ctx context.Context, _ jobs.Job) error { return h.Service.Live(ctx) }

// NewService wires the production clients from configuration.
func NewService(cfg Config, db *sql.DB, logger *slog.Logger) *Service {
	service := &Service{
		Config:   cfg,
		Store:    NewPostgresStore(db),
		Cards:    NewHTTPCardRenderer(cfg.CardBaseURL),
		Telegram: NewTelegramClient(cfg.TelegramBotToken),
		Logger:   logger,
	}
	if cfg.FacebookEnabled() {
		service.Facebook = NewFacebookClient(cfg.FacebookPageID, cfg.FacebookPageToken, cfg.FacebookGraphVersion)
	}
	if cfg.VideoEnabled {
		service.Video = FFmpegVideoBuilder{Path: cfg.FFmpegPath}
	}
	return service
}
