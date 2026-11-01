package access

import (
	"encoding/json"
	"errors"
	"sort"
)

var ErrCapabilityDenied = errors.New("capability denied")

type Tier string

const (
	TierGuest Tier = "guest"
	TierBasic Tier = "basic"
	TierPro   Tier = "pro"
)

const (
	CapabilityPublicRead              = "public.read"
	CapabilityAnalysisStandard        = "analysis.standard"
	CapabilityAnalysisAdvanced        = "analysis.advanced"
	CapabilityAdsHidden               = "ads.hidden"
	CapabilityPersonalRecommendations = "recommendations.personalized"
	CapabilityNotificationDigest      = "notifications.digest"
	CapabilityNotificationRealtime    = "notifications.realtime"
	CapabilityExpandedHistory         = "history.expanded"
	// CapabilityInPlayPredictions gates the in-play prediction payload. It is
	// granted implicitly to every signed-in account (see NewContext) because
	// the launch rule is "logged in", not "paying" -- but it is a real
	// capability rather than an `if userID > 0` scattered through handlers, so
	// a single account can be revoked through the existing grant tables when
	// it turns out to be a scraper.
	CapabilityInPlayPredictions = "predictions.inplay"
)

type Principal struct {
	UserID         int64
	GuestSessionID int64
	Tier           Tier
	Roles          []string
}

type Context struct {
	Principal    Principal
	AdsAllowed   bool
	capabilities map[string]json.RawMessage
}

func NewContext(principal Principal, capabilities map[string]json.RawMessage) Context {
	copyOfCapabilities := make(map[string]json.RawMessage, len(capabilities)+1)
	copyOfCapabilities[CapabilityPublicRead] = json.RawMessage("true")
	for key, value := range capabilities {
		copyOfCapabilities[key] = append(json.RawMessage(nil), value...)
	}
	// In-play predictions are the signed-in benefit, so every real account
	// gets the capability by default. An explicit entry in the snapshot or the
	// grant tables wins, which is how a per-account revocation works -- and
	// why the default is only applied when nothing was said.
	if principal.UserID > 0 && principal.Tier != TierGuest {
		if _, explicit := copyOfCapabilities[CapabilityInPlayPredictions]; !explicit {
			copyOfCapabilities[CapabilityInPlayPredictions] = json.RawMessage("true")
		}
	}
	return Context{
		Principal:    principal,
		AdsAllowed:   !enabled(copyOfCapabilities[CapabilityAdsHidden]),
		capabilities: copyOfCapabilities,
	}
}

func GuestContext(guestSessionID int64) Context {
	return NewContext(Principal{GuestSessionID: guestSessionID, Tier: TierGuest}, nil)
}

func (c Context) Allows(capability string) bool {
	return enabled(c.capabilities[capability])
}

func (c Context) Require(capability string) error {
	if !c.Allows(capability) {
		return ErrCapabilityDenied
	}
	return nil
}

func (c Context) CapabilityValue(capability string) (json.RawMessage, bool) {
	value, ok := c.capabilities[capability]
	return append(json.RawMessage(nil), value...), ok
}

func (c Context) CapabilityNames() []string {
	names := make([]string, 0, len(c.capabilities))
	for name := range c.capabilities {
		if c.Allows(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func enabled(value json.RawMessage) bool {
	if len(value) == 0 {
		return false
	}
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return false
	}
	switch typed := decoded.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed != ""
	default:
		return decoded != nil
	}
}
