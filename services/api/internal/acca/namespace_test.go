package acca

import "testing"

func TestNamespaceNeverSharesQAAndProductionKeys(t *testing.T) {
	if Key("qa:acca", "slip", "1") == Key("acca", "slip", "1") {
		t.Fatal("QA key collides with production")
	}
}
