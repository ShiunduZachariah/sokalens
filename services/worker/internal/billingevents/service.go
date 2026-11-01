package billingevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Service struct {
	store    Store
	decoders map[string]Decoder
	now      func() time.Time
}

func NewService(store Store, decoders map[string]Decoder, now func() time.Time) (Service, error) {
	if store == nil {
		return Service{}, errors.New("billing event store is required")
	}
	if len(decoders) == 0 {
		return Service{}, errors.New("at least one billing event decoder is required")
	}
	if now == nil {
		now = time.Now
	}
	return Service{store: store, decoders: decoders, now: now}, nil
}

func (s Service) ProcessNext(ctx context.Context) (ProcessResult, error) {
	result := ProcessResult{}
	err := s.store.WithinTransaction(ctx, func(tx TransactionStore) error {
		event, ok, err := tx.ClaimNext(ctx)
		if err != nil || !ok {
			return err
		}
		result.EventID = event.ID
		result.Processed = true
		now := s.now().UTC()
		if !event.SignatureVerified {
			result.Failed = true
			result.Reason = "signature not verified"
			return tx.MarkFailed(ctx, event.ID, result.Reason, now)
		}
		decoder := s.decoders[event.Provider]
		if decoder == nil {
			result.Failed = true
			result.Reason = "unsupported billing provider: " + event.Provider
			return tx.MarkFailed(ctx, event.ID, result.Reason, now)
		}
		update, err := decoder.Decode(event)
		if err != nil {
			result.Failed = true
			result.Reason = "decode billing event: " + err.Error()
			return tx.MarkFailed(ctx, event.ID, result.Reason, now)
		}
		if err := validateUpdate(update, false); err != nil {
			result.Failed = true
			result.Reason = err.Error()
			return tx.MarkFailed(ctx, event.ID, result.Reason, now)
		}

		existing, found, err := tx.LoadSubscriptionForUpdate(ctx, event, update.ProviderSubscriptionID)
		if err != nil {
			return err
		}
		if found {
			if update.UserID == 0 {
				update.UserID = existing.UserID
			}
			if update.UserID != existing.UserID {
				result.Failed = true
				result.Reason = "billing event user does not match existing subscription"
				return tx.MarkFailed(ctx, event.ID, result.Reason, now)
			}
			if !shouldApply(existing, update, decoder) {
				result.Ignored = true
				result.SubscriptionID = existing.ID
				result.Reason = "duplicate or out-of-order subscription event"
				return tx.MarkIgnored(ctx, event.ID, result.Reason, now)
			}
		}
		if err := validateUpdate(update, true); err != nil {
			result.Failed = true
			result.Reason = err.Error()
			return tx.MarkFailed(ctx, event.ID, result.Reason, now)
		}

		subscription, err := tx.UpsertSubscription(ctx, event, update)
		if err != nil {
			return err
		}
		result.SubscriptionID = subscription.ID
		if err := tx.AppendSubscriptionEvent(ctx, event, subscription, update, existing.Status); err != nil {
			return err
		}
		if err := tx.SyncEntitlements(ctx, subscription, update, entitlementsEnabled(update, now), now); err != nil {
			return err
		}
		if update.Dispute != nil {
			if strings.TrimSpace(update.Dispute.ProviderDisputeID) == "" || strings.TrimSpace(update.Dispute.Status) == "" {
				return tx.MarkFailed(ctx, event.ID, "billing dispute ID and status are required", now)
			}
			if err := tx.RecordDispute(ctx, event, subscription, *update.Dispute); err != nil {
				return err
			}
		}
		intent := update.Notification
		if intent == nil {
			generated := defaultNotification(event, update)
			intent = &generated
		}
		if err := tx.EnqueueNotification(ctx, subscription.UserID, *intent); err != nil {
			return err
		}
		if err := tx.MarkProcessed(ctx, event.ID, now); err != nil {
			return err
		}
		result.Applied = true
		return nil
	})
	return result, err
}

func shouldApply(existing Subscription, update SubscriptionUpdate, decoder Decoder) bool {
	if existing.ProviderEventTime.IsZero() {
		return true
	}
	if update.EffectiveAt.After(existing.ProviderEventTime) {
		return true
	}
	if update.EffectiveAt.Before(existing.ProviderEventTime) {
		return false
	}
	if update.Revision == existing.ProviderRevision {
		return false
	}
	return decoder.CompareRevision(existing.ProviderRevision, update.Revision) > 0
}

func entitlementsEnabled(update SubscriptionUpdate, now time.Time) bool {
	switch update.Status {
	case "active", "trialing", "scheduled_to_cancel":
		return update.CurrentPeriodEnd == nil || update.CurrentPeriodEnd.After(now)
	case "past_due", "payment_failed":
		return update.GraceEndsAt != nil && update.GraceEndsAt.After(now)
	default:
		return false
	}
}

func validateUpdate(update SubscriptionUpdate, requireUser bool) error {
	if requireUser && update.UserID <= 0 {
		return errors.New("billing event user ID must be positive")
	}
	if strings.TrimSpace(update.PlanKey) == "" {
		return errors.New("billing event plan key is required")
	}
	if strings.TrimSpace(update.ProviderSubscriptionID) == "" {
		return errors.New("provider subscription ID is required")
	}
	validStatus := map[string]bool{
		"incomplete": true, "active": true, "trialing": true, "past_due": true,
		"cancelled": true, "expired": true, "payment_failed": true,
		"scheduled_to_cancel": true, "suspended": true,
	}
	if !validStatus[update.Status] {
		return fmt.Errorf("unsupported subscription status %q", update.Status)
	}
	if update.EffectiveAt.IsZero() {
		return errors.New("billing event effective time is required")
	}
	return nil
}

func defaultNotification(event InboxEvent, update SubscriptionUpdate) NotificationIntent {
	payload, _ := json.Marshal(map[string]any{
		"title":    "Subscription updated",
		"body":     "Your SokaLens subscription status is now " + update.Status + ".",
		"status":   update.Status,
		"plan_key": update.PlanKey,
	})
	return NotificationIntent{
		Channel: "in_app", Category: "subscription_status",
		DedupeKey: fmt.Sprintf("billing:%s:%s:%s:status", event.Provider, event.Environment, event.ProviderEventID),
		Payload:   payload, Priority: 30,
	}
}

type CanonicalJSONDecoder struct{}

func (CanonicalJSONDecoder) Decode(event InboxEvent) (SubscriptionUpdate, error) {
	var update SubscriptionUpdate
	err := json.Unmarshal(event.Payload, &update)
	return update, err
}

func (CanonicalJSONDecoder) CompareRevision(current, incoming string) int {
	if current == incoming {
		return 0
	}
	// Revisions are provider-specific. Without a provider adapter that understands
	// their order, equal-time conflicting revisions are conservatively ignored.
	return -1
}
