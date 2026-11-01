// Package ops exposes read-only operational views (jobs and provider
// requests) for the admin control plane.
package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type JobSummary struct {
	ID          int64      `json:"id"`
	JobKey      string     `json:"job_key"`
	Queue       string     `json:"queue"`
	Status      string     `json:"status"`
	Priority    int        `json:"priority"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`
	LastError   string     `json:"last_error"`
}

type ProviderRequest struct {
	ID            int64     `json:"id"`
	Provider      string    `json:"provider"`
	Endpoint      string    `json:"endpoint"`
	StatusCode    int       `json:"status_code"`
	LatencyMs     int64     `json:"latency_ms"`
	QuotaCost     int       `json:"quota_cost"`
	RequestedAt   time.Time `json:"requested_at"`
	ErrorCategory string    `json:"error_category"`
}

type Setting struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedBy int64           `json:"updated_by"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type Notification struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Category   string     `json:"category"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	ActionURL  string     `json:"action_url"`
	EntityType string     `json:"entity_type"`
	EntityID   string     `json:"entity_id"`
	ReadAt     *time.Time `json:"read_at"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

type NotificationOutbox struct {
	ID             int64      `json:"id"`
	UserID         int64      `json:"user_id"`
	NotificationID *int64     `json:"notification_id"`
	Channel        string     `json:"channel"`
	Category       string     `json:"category"`
	Priority       int        `json:"priority"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	MaxAttempts    int        `json:"max_attempts"`
	LastError      string     `json:"last_error"`
	SentAt         *time.Time `json:"sent_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

type Source struct {
	db *sql.DB
}

func NewSource(db *sql.DB) Source {
	return Source{db: db}
}

func (s Source) ListJobs(ctx context.Context, limit int) ([]JobSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_key, COALESCE(queue, ''), status, COALESCE(priority, 0), attempts, COALESCE(max_attempts, 0), created_at, completed_at, COALESCE(last_error, '')
FROM jobs
ORDER BY id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []JobSummary{}
	for rows.Next() {
		var job JobSummary
		var completedAt sql.NullTime
		if err := rows.Scan(&job.ID, &job.JobKey, &job.Queue, &job.Status, &job.Priority, &job.Attempts, &job.MaxAttempts, &job.CreatedAt, &completedAt, &job.LastError); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Time
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s Source) ListProviderRequests(ctx context.Context, limit int) ([]ProviderRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, provider, endpoint, COALESCE(status_code, 0), COALESCE(latency_ms, 0), COALESCE(quota_cost, 0), requested_at, COALESCE(error_category, '')
FROM provider_request_logs
ORDER BY id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	requests := []ProviderRequest{}
	for rows.Next() {
		var request ProviderRequest
		if err := rows.Scan(&request.ID, &request.Provider, &request.Endpoint, &request.StatusCode, &request.LatencyMs, &request.QuotaCost, &request.RequestedAt, &request.ErrorCategory); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (s Source) ListSettings(ctx context.Context) ([]Setting, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT key, value, COALESCE(updated_by, 0), updated_at
FROM app_settings
ORDER BY key ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := []Setting{}
	for rows.Next() {
		var setting Setting
		var rawValue string
		if err := rows.Scan(&setting.Key, &rawValue, &setting.UpdatedBy, &setting.UpdatedAt); err != nil {
			return nil, err
		}
		setting.Value = json.RawMessage(rawValue)
		settings = append(settings, setting)
	}
	return settings, rows.Err()
}

func (s Source) ListNotifications(ctx context.Context, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, COALESCE(category, ''), COALESCE(title, ''), COALESCE(body, ''), COALESCE(action_url, ''), COALESCE(entity_type, ''), COALESCE(entity_id, ''), read_at, created_at, expires_at
FROM notifications
ORDER BY id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	notifications := []Notification{}
	for rows.Next() {
		var notification Notification
		var readAt, expiresAt sql.NullTime
		if err := rows.Scan(&notification.ID, &notification.UserID, &notification.Category, &notification.Title, &notification.Body, &notification.ActionURL, &notification.EntityType, &notification.EntityID, &readAt, &notification.CreatedAt, &expiresAt); err != nil {
			return nil, err
		}
		if readAt.Valid {
			notification.ReadAt = &readAt.Time
		}
		if expiresAt.Valid {
			notification.ExpiresAt = &expiresAt.Time
		}
		notifications = append(notifications, notification)
	}
	return notifications, rows.Err()
}

func (s Source) ListNotificationOutbox(ctx context.Context, limit int) ([]NotificationOutbox, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, notification_id, COALESCE(channel, ''), COALESCE(category, ''), COALESCE(priority, 0), COALESCE(status, ''), COALESCE(attempts, 0), COALESCE(max_attempts, 0), COALESCE(last_error, ''), sent_at, created_at
FROM notification_outbox
ORDER BY id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	outbox := []NotificationOutbox{}
	for rows.Next() {
		var item NotificationOutbox
		var notificationID sql.NullInt64
		var sentAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.UserID, &notificationID, &item.Channel, &item.Category, &item.Priority, &item.Status, &item.Attempts, &item.MaxAttempts, &item.LastError, &sentAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		if notificationID.Valid {
			item.NotificationID = &notificationID.Int64
		}
		if sentAt.Valid {
			item.SentAt = &sentAt.Time
		}
		outbox = append(outbox, item)
	}
	return outbox, rows.Err()
}
