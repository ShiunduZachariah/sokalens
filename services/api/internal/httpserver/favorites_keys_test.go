package httpserver

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
)

// favoritesHandler needs a *accounts.Service over a real *SQLRepository, so
// the two ordering properties below are checked against the source. They are
// guards on structure, not substitutes for the behaviour tests above.
func readHandlerSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("account_handlers.go")
	if err != nil {
		t.Fatalf("read handler source: %v", err)
	}
	return string(source)
}

func sliceBetween(t *testing.T, source, start, end string) string {
	t.Helper()
	from := strings.Index(source, start)
	if from < 0 {
		t.Fatalf("could not find %q in the handler source", start)
	}
	rest := source[from+len(start):]
	to := strings.Index(rest, end)
	if to < 0 {
		return rest
	}
	return rest[:to]
}

func TestFavoriteKeyResponsesCarryOnlyTheEntityIdentity(t *testing.T) {
	items := []accounts.Favorite{
		{ID: 1, EntityType: "match", EntityID: "2014779", DisplayName: "Bournemouth vs Liverpool", CreatedAt: time.Now()},
		{ID: 2, EntityType: "league", EntityID: "premier-league", DisplayName: "Premier League"},
	}
	keys := favoriteKeyResponses(items)
	if len(keys) != 2 {
		t.Fatalf("keys=%d want=2", len(keys))
	}
	for index, want := range []accounts.Favorite{items[0], items[1]} {
		if keys[index]["entity_type"] != want.EntityType || keys[index]["entity_id"] != want.EntityID {
			t.Fatalf("key %d=%+v want type=%q id=%q", index, keys[index], want.EntityType, want.EntityID)
		}
		// The point of this view is the fields it omits: anything that needs a
		// catalog lookup per favorite must not appear here.
		for _, absent := range []string{"href", "matches", "context_label", "name", "notifications_enabled"} {
			if _, present := keys[index][absent]; present {
				t.Fatalf("key %d unexpectedly carries %q: %+v", index, absent, keys[index])
			}
		}
	}
}

func TestFavoriteKeyResponsesSerializeAsAnArrayWhenEmpty(t *testing.T) {
	// A viewer with nothing saved must serialize as [], not null: the web
	// client reads the array straight into the saved set.
	encoded, err := json.Marshal(map[string]any{"favorites": favoriteKeyResponses(nil)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(encoded); got != `{"favorites":[]}` {
		t.Fatalf("empty keys payload=%s", got)
	}
}

func TestFavoritesKeysViewSkipsTheContextFanOut(t *testing.T) {
	// Guards the ordering inside favoritesHandler: the keys branch has to
	// return before the goroutine fan-out that builds match previews,
	// otherwise the cheap view costs exactly what the expensive one does.
	source := readHandlerSource(t)
	handler := sliceBetween(t, source, "func (d accountDependencies) favoritesHandler()", "\nfunc ")
	keysAt := strings.Index(handler, `r.URL.Query().Get("view") == "keys"`)
	fanOutAt := strings.Index(handler, "favoriteContextResponse")
	if keysAt < 0 || fanOutAt < 0 {
		t.Fatalf("keys branch at %d, fan-out at %d", keysAt, fanOutAt)
	}
	if keysAt > fanOutAt {
		t.Fatal("the keys view runs the per-favorite context fan-out before returning")
	}
	if !strings.Contains(handler[keysAt:fanOutAt], "return") {
		t.Fatal("the keys branch does not return before the context fan-out")
	}
}

func TestFavoritesHandlerSupportsRemoval(t *testing.T) {
	// The save button's unsave path depends on this method existing; it was
	// always here, which is why the fix needed no new endpoint.
	handler := sliceBetween(t, readHandlerSource(t), "func (d accountDependencies) favoritesHandler()", "\nfunc ")
	if !strings.Contains(handler, "case http.MethodDelete:") || !strings.Contains(handler, "RemoveFavorite") {
		t.Fatal("favorites handler no longer supports DELETE")
	}
}
