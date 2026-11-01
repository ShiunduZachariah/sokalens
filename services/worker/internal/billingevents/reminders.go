package billingevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

const (
	SubscriptionReminderJobKey = "billing.queue-subscription-reminders"
	BillingQueue               = "billing"
	defaultReminderBatchSize   = 250
)

var defaultRenewalReminderDays = []int{7, 3, 1}

type ReminderSubscription struct {
	ID                int64
	UserID            int64
	PlanKey           string
	Status            string
	CurrentPeriodEnd  *time.Time
	CancelAtPeriodEnd bool
	CancelledAt       *time.Time
	Email             string
	EmailVerified     bool
}

type QueuedReminderIntent struct {
	UserID    int64
	Channel   string
	Category  string
	DedupeKey string
	Payload   json.RawMessage
	Priority  int
}

type ReminderStore interface {
	LoadReminderDays(context.Context) ([]int, error)
	ListReminderSubscriptions(context.Context, time.Time, []int, int) ([]ReminderSubscription, error)
	EnqueueReminderIntents(context.Context, []QueuedReminderIntent) (int, error)
}

type ReminderOptions struct {
	BatchSize int
}

type ReminderService struct {
	store     ReminderStore
	batchSize int
	now       func() time.Time
}

func NewReminderService(store ReminderStore, opts ReminderOptions, now func() time.Time) (ReminderService, error) {
	if store == nil {
		return ReminderService{}, errors.New("subscription reminder store is required")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultReminderBatchSize
	}
	if opts.BatchSize > 1000 {
		return ReminderService{}, errors.New("subscription reminder batch size must not exceed 1000")
	}
	if now == nil {
		now = time.Now
	}
	return ReminderService{store: store, batchSize: opts.BatchSize, now: now}, nil
}

func (s ReminderService) Queue(ctx context.Context, limit int) (int, error) {
	if s.store == nil {
		return 0, errors.New("subscription reminder store is required")
	}
	if limit <= 0 {
		limit = s.batchSize
	}
	if limit <= 0 || limit > 1000 {
		return 0, errors.New("subscription reminder limit must be between 1 and 1000")
	}
	days, err := s.store.LoadReminderDays(ctx)
	if err != nil {
		return 0, err
	}
	days, err = normalizeReminderDays(days)
	if err != nil {
		return 0, err
	}
	now := s.now().UTC()
	subscriptions, err := s.store.ListReminderSubscriptions(ctx, now, days, limit)
	if err != nil {
		return 0, err
	}
	intents, err := planReminderIntents(subscriptions, days, now)
	if err != nil {
		return 0, err
	}
	if len(intents) == 0 {
		return 0, nil
	}
	return s.store.EnqueueReminderIntents(ctx, intents)
}

type ReminderJob struct {
	Service ReminderService
}

type reminderJobPayload struct {
	Limit int `json:"limit"`
}

func (j ReminderJob) Run(ctx context.Context, job jobs.Job) error {
	if j.Service.store == nil {
		return errors.New("subscription reminder service is required")
	}
	payload := reminderJobPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	if payload.Limit < 0 || payload.Limit > 1000 {
		return errors.New("subscription reminder limit must be zero or between 1 and 1000")
	}
	_, err := j.Service.Queue(ctx, payload.Limit)
	return err
}

func normalizeReminderDays(days []int) ([]int, error) {
	if len(days) == 0 {
		days = defaultRenewalReminderDays
	}
	unique := make(map[int]struct{}, len(days))
	for _, day := range days {
		if day <= 0 || day > 365 {
			return nil, fmt.Errorf("subscription reminder day %d must be between 1 and 365", day)
		}
		unique[day] = struct{}{}
	}
	normalized := make([]int, 0, len(unique))
	for day := range unique {
		normalized = append(normalized, day)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(normalized)))
	return normalized, nil
}

func planReminderIntents(subscriptions []ReminderSubscription, days []int, now time.Time) ([]QueuedReminderIntent, error) {
	intents := make([]QueuedReminderIntent, 0, len(subscriptions)*4)
	for _, subscription := range subscriptions {
		cancelScheduled := subscription.CancelAtPeriodEnd || subscription.Status == "scheduled_to_cancel"
		if cancelScheduled && (subscription.CurrentPeriodEnd == nil || subscription.CurrentPeriodEnd.After(now)) {
			planned, err := reminderIntents(subscription, "cancellation_scheduled", 0, subscription.CurrentPeriodEnd,
				"Cancellation scheduled",
				"Your SokaLens subscription is scheduled to end at the close of the current billing period.",
				25,
			)
			if err != nil {
				return nil, err
			}
			intents = append(intents, planned...)
		}

		if subscription.CurrentPeriodEnd != nil {
			for _, day := range days {
				if !sameUTCDay(*subscription.CurrentPeriodEnd, now.AddDate(0, 0, day)) {
					continue
				}
				kind := "renewal_reminder"
				title := "Subscription renewal reminder"
				body := fmt.Sprintf("Your SokaLens subscription renews in %d %s.", day, dayLabel(day))
				if cancelScheduled {
					kind = "expiry_reminder"
					title = "Subscription ending soon"
					body = fmt.Sprintf("Your SokaLens subscription ends in %d %s.", day, dayLabel(day))
				} else if subscription.Status != "active" && subscription.Status != "trialing" {
					continue
				}
				planned, err := reminderIntents(subscription, kind, day, subscription.CurrentPeriodEnd, title, body, 40)
				if err != nil {
					return nil, err
				}
				intents = append(intents, planned...)
			}
		}

		if subscription.Status == "cancelled" || subscription.Status == "expired" {
			endedAt := subscription.CurrentPeriodEnd
			if endedAt == nil {
				endedAt = subscription.CancelledAt
			}
			if endedAt != nil && !endedAt.After(now) {
				planned, err := reminderIntents(subscription, "ended", 0, endedAt,
					"Subscription ended",
					"Your SokaLens subscription has ended. You can renew it at any time from billing settings.",
					20,
				)
				if err != nil {
					return nil, err
				}
				intents = append(intents, planned...)
			}
		}
	}
	return intents, nil
}

func reminderIntents(subscription ReminderSubscription, kind string, reminderDays int, effectiveAt *time.Time, title, body string, priority int) ([]QueuedReminderIntent, error) {
	eventToken := kind
	if reminderDays > 0 {
		eventToken += "-" + strconv.Itoa(reminderDays) + "d"
	}
	periodToken := "none"
	periodEnd := ""
	if effectiveAt != nil {
		periodToken = strconv.FormatInt(effectiveAt.UTC().Unix(), 10)
		periodEnd = effectiveAt.UTC().Format(time.RFC3339)
	}
	keyPrefix := fmt.Sprintf("billing:subscription:%d:%s:%s", subscription.ID, periodToken, eventToken)
	common := map[string]any{
		"title": title, "body": body, "status": subscription.Status,
		"plan_key": subscription.PlanKey, "period_end": periodEnd,
		"reminder_days": reminderDays, "action_url": "/subscription",
		"entity_type": "subscription", "entity_id": strconv.FormatInt(subscription.ID, 10),
	}
	inAppPayload, err := json.Marshal(common)
	if err != nil {
		return nil, err
	}
	intents := []QueuedReminderIntent{{
		UserID: subscription.UserID, Channel: "in_app", Category: "subscription_" + kind,
		DedupeKey: keyPrefix + ":in_app", Payload: inAppPayload, Priority: priority,
	}}
	if subscription.EmailVerified && strings.TrimSpace(subscription.Email) != "" {
		emailPayload, err := json.Marshal(map[string]string{
			"to": strings.TrimSpace(subscription.Email), "subject": title, "text": body + " Visit your SokaLens billing settings for details.",
		})
		if err != nil {
			return nil, err
		}
		intents = append(intents, QueuedReminderIntent{
			UserID: subscription.UserID, Channel: "email", Category: "subscription_" + kind,
			DedupeKey: keyPrefix + ":email", Payload: emailPayload, Priority: priority,
		})
	}
	return intents, nil
}

func sameUTCDay(left, right time.Time) bool {
	left = left.UTC()
	right = right.UTC()
	return left.Year() == right.Year() && left.YearDay() == right.YearDay()
}

func dayLabel(days int) string {
	if days == 1 {
		return "day"
	}
	return "days"
}
