package marketing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
)

// Card kinds. Each has a template in apps/web/app/lib/campaignCard.tsx, and
// the web route renders nothing it does not recognise.
const (
	KindPicks   = "picks"
	KindResults = "results"
	KindGoal    = "goal"
	KindStat    = "stat"
	KindPreview = "preview"
	KindRecord  = "record"
)

// Card formats: a square for feeds and chats, a 9:16 story for WhatsApp
// Status, TikTok, Reels and Shorts.
const (
	FormatSquare = "square"
	FormatStory  = "story"
)

// cardVersion is checked by the web route; bump it if a field changes meaning.
const cardVersion = 1

// MaxTokenLength bounds what the web route will accept. A card that needs more
// than this is carrying too much text to be read on a phone anyway.
const MaxTokenLength = 6000

// Card is the signed content of one image. Field names are short because the
// whole card travels in a URL; apps/web/app/lib/campaignToken.ts mirrors them.
type Card struct {
	Version  int        `json:"v"`
	Kind     string     `json:"k"`
	Format   string     `json:"f"`
	Eyebrow  string     `json:"e,omitempty"`
	Headline string     `json:"h,omitempty"`
	Body     string     `json:"b,omitempty"`
	Note     string     `json:"n,omitempty"`
	Items    []CardItem `json:"i,omitempty"`
	Match    *CardMatch `json:"m,omitempty"`
	Lines    []string   `json:"l,omitempty"`
	Probs    *CardProbs `json:"p,omitempty"`
}

// CardItem is one pick, on a picks or results card.
type CardItem struct {
	League     string `json:"lg,omitempty"`
	Home       string `json:"h"`
	Away       string `json:"a"`
	HomeLogo   string `json:"hl,omitempty"`
	AwayLogo   string `json:"al,omitempty"`
	Kickoff    string `json:"k,omitempty"`
	Pick       string `json:"p"`
	Confidence int    `json:"c,omitempty"`
	Score      string `json:"s,omitempty"`
	// Result is won, lost, void or pending.
	Result string `json:"r,omitempty"`
}

// CardMatch is the single fixture on a goal or preview card.
type CardMatch struct {
	League   string `json:"lg,omitempty"`
	Home     string `json:"h"`
	Away     string `json:"a"`
	HomeLogo string `json:"hl,omitempty"`
	AwayLogo string `json:"al,omitempty"`
	Kickoff  string `json:"k,omitempty"`
	Score    string `json:"s,omitempty"`
	Minute   string `json:"mi,omitempty"`
	Event    string `json:"ev,omitempty"`
	Pick     string `json:"p,omitempty"`
}

// CardProbs are live win chances in basis points, summing to 10,000.
type CardProbs struct {
	Home int `json:"h"`
	Draw int `json:"d"`
	Away int `json:"a"`
}

// SignCard returns "<payload>.<signature>", both base64url without padding.
//
// The signature covers the encoded payload exactly as sent, so the verifier
// never has to re-serialise JSON and agree with Go on key order or spacing.
func SignCard(secret string, card Card) (string, error) {
	if secret == "" {
		return "", errors.New("card secret is empty")
	}
	card.Version = cardVersion
	if card.Format == "" {
		card.Format = FormatSquare
	}
	raw, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	token := payload + "." + signPayload(secret, payload)
	if len(token) > MaxTokenLength {
		return "", errors.New("card is too large to sign")
	}
	return token, nil
}

func signPayload(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CardPath is the web route for a signed token, relative to an origin.
func CardPath(token string) string {
	return "/og/campaign?t=" + url.QueryEscape(token)
}
