package marketing

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// cardVector is shared with apps/web/app/lib/campaignToken.test.mjs. If the
// encoding changes on either side, both tests must change together, or the
// web route stops verifying the worker's cards and every post fails.
const (
	cardVectorSecret = "test-secret-that-is-at-least-32-characters"
	cardVectorToken  = "eyJ2IjoxLCJrIjoic3RhdCIsImYiOiJzcXVhcmUiLCJlIjoiT3VyIHJlY29yZCIsImgiOiI2OCUiLCJiIjoib2Ygb3VyIHRvcC1sZWFndWUgbWF0Y2ggcmVzdWx0IHBpY2tzIHdlcmUgcmlnaHQiLCJuIjoiMTM2IG9mIDIwMCBncmFkZWQgcGlja3MgwrcgbGFzdCA5MCBkYXlzIn0.EmYlgKih11hM1HI59tnklOvfmUJJhV4z8YmSP2mYptE"
)

func vectorCard() Card {
	return Card{
		Kind: KindStat, Format: FormatSquare, Eyebrow: "Our record", Headline: "68%",
		Body: "of our top-league match result picks were right", Note: "136 of 200 graded picks · last 90 days",
	}
}

func TestSignCardMatchesTheSharedVector(t *testing.T) {
	token, err := SignCard(cardVectorSecret, vectorCard())
	if err != nil {
		t.Fatal(err)
	}
	if token != cardVectorToken {
		t.Fatalf("token changed; update the web test vector too:\n%s", token)
	}
}

func TestSignCardCoversThePayload(t *testing.T) {
	token, err := SignCard(cardVectorSecret, vectorCard())
	if err != nil {
		t.Fatal(err)
	}
	payload, signature, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatal("token has no signature")
	}
	if !hmac.Equal([]byte(signature), []byte(signPayload(cardVectorSecret, payload))) {
		t.Fatal("signature does not cover the payload")
	}
	if signPayload("another-secret-that-is-also-32-characters", payload) == signature {
		t.Fatal("a different secret must not produce the same signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Card
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Version != 1 || decoded.Kind != KindStat {
		t.Fatalf("payload = %s (%v)", raw, err)
	}
}

func TestSignCardRefusesOversizeCards(t *testing.T) {
	card := vectorCard()
	card.Body = strings.Repeat("x", MaxTokenLength)
	if _, err := SignCard(cardVectorSecret, card); err == nil {
		t.Fatal("an oversize card must be refused")
	}
	if _, err := SignCard("", vectorCard()); err == nil {
		t.Fatal("an empty secret must be refused")
	}
}
