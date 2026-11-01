package notifications

import (
	"strings"
	"testing"
)

func TestInAppMaterializationIsLeaseFencedAndRetrySafe(t *testing.T) {
	for _, required := range []string{
		"WITH claimed AS",
		"lease_token = $2::uuid",
		"WHERE claimed.notification_id IS NULL",
		"SET notification_id = COALESCE(claimed.notification_id, created.id)",
		"SELECT notification_id FROM linked",
	} {
		if !strings.Contains(materializeInAppSQL, required) {
			t.Fatalf("materialization SQL missing %q", required)
		}
	}
}
