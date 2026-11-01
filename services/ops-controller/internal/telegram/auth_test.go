package telegram

import (
	"errors"
	"strings"
	"testing"
)

const (
	authChatID = int64(-1001234567890) // group IDs are negative
	ownerID    = int64(778899)
	deputyID   = int64(112233)
)

// authUpdate builds an update from one chat and one human sender.
func authUpdate(chatID, userID int64) Update {
	return Update{
		UpdateID: 1,
		Message: &Message{
			MessageID: 7,
			From:      &User{ID: userID, Username: "operator"},
			Chat:      Chat{ID: chatID, Type: "supergroup"},
			Text:      "/status",
		},
	}
}

func TestAuthorizeRequiresBothChatAndUser(t *testing.T) {
	access := NewAccess(authChatID, []int64{ownerID, deputyID})

	tests := []struct {
		name   string
		update Update
		want   bool
		reason string
	}{
		{
			name:   "right chat and right user",
			update: authUpdate(authChatID, ownerID),
			want:   true,
		},
		{
			name:   "right chat and a second allowlisted user",
			update: authUpdate(authChatID, deputyID),
			want:   true,
		},
		{
			// Anybody can be added to a group. Membership is not authority.
			name:   "right chat but the user is not allowlisted",
			update: authUpdate(authChatID, 999000111),
			want:   false,
			reason: ReasonUserNotAllowed,
		},
		{
			// A compromised owner account must not be able to drive the bot
			// by DM, out of sight of the audited group.
			name:   "right user but the wrong chat",
			update: authUpdate(555444333, ownerID),
			want:   false,
			reason: ReasonChatNotAllowed,
		},
		{
			name:   "both wrong",
			update: authUpdate(555444333, 999000111),
			want:   false,
			reason: ReasonChatNotAllowed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := access.Authorize(tc.update)
			if got.Authorized != tc.want {
				t.Fatalf("Authorized = %v, want %v (reason %q)", got.Authorized, tc.want, got.Reason)
			}
			if got.Reason != tc.reason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tc.reason)
			}
			if got.Authorized != access.Allowed(tc.update) {
				t.Fatalf("Allowed disagrees with Authorize")
			}
		})
	}
}

func TestEmptyAllowlistDeniesEveryone(t *testing.T) {
	// An empty allowlist is a deny-all. Reading it as "allow all" would hand
	// the bridge to the whole group the moment the config went missing.
	for _, allowed := range [][]int64{nil, {}, {0}, {-5}} {
		access := NewAccess(authChatID, allowed)
		if access.AllowlistSize() != 0 {
			t.Fatalf("allowlist %v produced %d entries, want 0", allowed, access.AllowlistSize())
		}
		d := access.Authorize(authUpdate(authChatID, ownerID))
		if d.Authorized {
			t.Fatalf("allowlist %v authorized a sender", allowed)
		}
		if d.Reason != ReasonEmptyAllowlist {
			t.Fatalf("Reason = %q, want %q", d.Reason, ReasonEmptyAllowlist)
		}
	}
}

func TestValidateFailsClosedOnUnusableConfiguration(t *testing.T) {
	if err := NewAccess(authChatID, nil).Validate(); !errors.Is(err, ErrEmptyAllowlist) {
		t.Fatalf("empty allowlist: err = %v, want ErrEmptyAllowlist", err)
	}
	if err := NewAccess(0, []int64{ownerID}).Validate(); !errors.Is(err, ErrInvalidChatID) {
		t.Fatalf("zero chat id: err = %v, want ErrInvalidChatID", err)
	}
	if err := NewAccess(authChatID, []int64{ownerID}).Validate(); err != nil {
		t.Fatalf("valid configuration: err = %v, want nil", err)
	}
}

func TestBotSendersAreDenied(t *testing.T) {
	access := NewAccess(authChatID, []int64{ownerID})
	u := authUpdate(authChatID, ownerID)
	u.Message.From.IsBot = true

	d := access.Authorize(u)
	if d.Authorized {
		t.Fatal("a bot sender was authorized")
	}
	if d.Reason != ReasonBotSender {
		t.Fatalf("Reason = %q, want %q", d.Reason, ReasonBotSender)
	}
}

func TestNilSenderAndNilMessageAreDenied(t *testing.T) {
	access := NewAccess(authChatID, []int64{ownerID})

	noSender := authUpdate(authChatID, ownerID)
	noSender.Message.From = nil
	if d := access.Authorize(noSender); d.Authorized || d.Reason != ReasonNoSender {
		t.Fatalf("nil sender: %+v", d)
	}

	noMessage := Update{UpdateID: 2}
	if d := access.Authorize(noMessage); d.Authorized || d.Reason != ReasonNoMessage {
		t.Fatalf("nil message: %+v", d)
	}
}

func TestNonPositiveSenderIDIsDenied(t *testing.T) {
	// Zero is what an absent JSON "id" decodes to. It must never match an
	// allowlist entry, and NewAccess drops zeros so it cannot.
	access := NewAccess(authChatID, []int64{ownerID})
	d := access.Authorize(authUpdate(authChatID, 0))
	if d.Authorized || d.Reason != ReasonInvalidUser {
		t.Fatalf("zero user id: %+v", d)
	}
}

func TestDecisionCarriesIdentityForTheLedgerAndBoundsTheUsername(t *testing.T) {
	access := NewAccess(authChatID, []int64{ownerID})

	u := authUpdate(555444333, 999000111)
	u.Message.From.Username = strings.Repeat("u", 300)

	d := access.Authorize(u)
	if d.Authorized {
		t.Fatal("unexpected authorization")
	}
	// Rejections are still recorded, so the identity fields must survive.
	if d.ChatID != 555444333 || d.UserID != 999000111 {
		t.Fatalf("identity not carried: %+v", d)
	}
	if got := len([]rune(d.Username)); got != maxUsernameChars {
		t.Fatalf("username length = %d, want %d", got, maxUsernameChars)
	}
}

func TestAuthorizationIgnoresUsernames(t *testing.T) {
	// Usernames are reassignable: releasing @ops_owner lets anybody claim it.
	// An allowlisted ID with someone else's username still passes; a
	// non-allowlisted ID wearing the owner's username still fails.
	access := NewAccess(authChatID, []int64{ownerID})

	impostor := authUpdate(authChatID, 424242)
	impostor.Message.From.Username = "ops_owner"
	if access.Allowed(impostor) {
		t.Fatal("a username matched instead of an ID")
	}

	renamed := authUpdate(authChatID, ownerID)
	renamed.Message.From.Username = "somebody_else"
	if !access.Allowed(renamed) {
		t.Fatal("a renamed but allowlisted user was denied")
	}
}
