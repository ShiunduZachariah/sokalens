package telegram

import "errors"

// ErrEmptyAllowlist is returned by Authorizer.Validate when the bridge is
// configured with no allowed user IDs. An empty allowlist denies every update;
// it must never be read as "allow all". Boot fails closed on this rather than
// starting a poller that can never authorize anybody (spec §8, plan "Config").
var ErrEmptyAllowlist = errors.New("telegram: allowlist is empty")

// ErrInvalidChatID is returned when the configured chat ID is zero. Telegram
// never issues chat ID 0, so a zero here is an unset config value that would
// otherwise match nothing (or, worse, an unset Chat.ID).
var ErrInvalidChatID = errors.New("telegram: chat id is not configured")

// Denial reasons. These are fixed labels: they are written to the forensic
// ledger and used as metric-adjacent strings, so they never carry any part of
// the untrusted update.
const (
	ReasonNoMessage      = "no_message"
	ReasonNoSender       = "no_sender"
	ReasonBotSender      = "bot_sender"
	ReasonInvalidUser    = "invalid_user_id"
	ReasonChatNotAllowed = "chat_not_allowed"
	ReasonEmptyAllowlist = "empty_allowlist"
	ReasonUserNotAllowed = "user_not_allowed"
)

// maxUsernameChars bounds the username copied out of an update. It is
// attacker-controlled and lands in ops_telegram_updates.username.
const maxUsernameChars = 64

// Access answers one question: may this update execute a command?
//
// Authorization requires BOTH the configured chat and an allowlisted numeric
// user ID. Neither alone is sufficient:
//
//   - Chat ID alone would hand control to anybody who is added to the group.
//   - User ID alone would let a compromised account drive the bot by DM,
//     outside the group where every command is visible to the other operators.
//
// Usernames are never consulted: they are mutable and, once released,
// reassignable to a different account (spec §8).
//
// Access implements the poller's Authorizer interface.
type Access struct {
	chatID  int64
	allowed map[int64]struct{}
}

var _ Authorizer = (*Access)(nil)

// NewAccess builds the allowlist set. Non-positive IDs are dropped: Telegram
// user IDs are always positive, and keeping a zero would make a sender-less or
// malformed update match the set.
func NewAccess(chatID int64, allowedUserIDs []int64) *Access {
	allowed := make(map[int64]struct{}, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		if id <= 0 {
			continue
		}
		allowed[id] = struct{}{}
	}
	return &Access{chatID: chatID, allowed: allowed}
}

// Validate reports a configuration that can never authorize anything. Call it
// at boot so the failure is a loud config error instead of a bot that silently
// ignores its owner.
func (a *Access) Validate() error {
	if a == nil {
		return ErrEmptyAllowlist
	}
	if a.chatID == 0 {
		return ErrInvalidChatID
	}
	if len(a.allowed) == 0 {
		return ErrEmptyAllowlist
	}
	return nil
}

// ChatID is the single chat the bridge accepts commands from.
func (a *Access) ChatID() int64 { return a.chatID }

// AllowlistSize is the number of distinct allowed user IDs.
func (a *Access) AllowlistSize() int { return len(a.allowed) }

// Allowed is the boolean form of Authorize, and implements the poller's
// Authorizer interface.
func (a *Access) Allowed(update Update) bool { return a.Authorize(update).Authorized }

// Decision is the outcome of one authorization check. The identity fields are
// filled in whenever they could be read, because rejected updates are still
// written to the forensic ledger.
type Decision struct {
	Authorized bool
	// Reason is one of the Reason* constants, empty when Authorized.
	Reason   string
	UserID   int64
	ChatID   int64
	Username string
}

// Authorize applies the two-factor rule. It never returns an error: every
// rejection is a Decision the caller must record and then answer with silence
// (spec §8 — any reply confirms the bot exists).
func (a *Access) Authorize(update Update) Decision {
	msg := update.Message
	if msg == nil {
		return Decision{Reason: ReasonNoMessage}
	}

	d := Decision{ChatID: msg.Chat.ID}

	// A message with no sender is a channel post or a service message. There
	// is no identity to check, so there is nothing that can be authorized.
	if msg.From == nil {
		return Decision{Reason: ReasonNoSender, ChatID: d.ChatID}
	}

	d.UserID = msg.From.ID
	d.Username = TruncateField(msg.From.Username, maxUsernameChars)

	// Bots never drive the bridge. Another bot in the group posting text that
	// happens to start with a slash must not become a command.
	if msg.From.IsBot {
		d.Reason = ReasonBotSender
		return d
	}
	if msg.From.ID <= 0 {
		d.Reason = ReasonInvalidUser
		return d
	}

	// Condition 1: the audited group.
	if a.chatID == 0 || msg.Chat.ID != a.chatID {
		d.Reason = ReasonChatNotAllowed
		return d
	}

	// Condition 2: an allowlisted numeric user ID. An empty allowlist is a
	// deny-all, reported distinctly so a misconfiguration is diagnosable.
	if len(a.allowed) == 0 {
		d.Reason = ReasonEmptyAllowlist
		return d
	}
	if _, ok := a.allowed[msg.From.ID]; !ok {
		d.Reason = ReasonUserNotAllowed
		return d
	}

	d.Authorized = true
	return d
}
