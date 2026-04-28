package database

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeAndUpgradeTiersDataRemovesLegacyTokenFields(t *testing.T) {
	raw := []byte(`{"tiers":[{"name":"legacy","min_tokens":32000,"max_tokens":128000,"input_price":1.2,"output_price":2.4}],"currency":"CNY"}`)

	data, changed := decodeAndUpgradeTiersData(raw)

	if !changed {
		t.Fatal("expected legacy JSON to be marked changed")
	}
	if len(data.Tiers) != 1 {
		t.Fatalf("tiers=%d, want 1", len(data.Tiers))
	}
	tier := data.Tiers[0]
	if tier.InputMin != 32000 {
		t.Fatalf("input_min=%d, want 32000", tier.InputMin)
	}
	if tier.InputMax == nil || *tier.InputMax != 128000 {
		t.Fatalf("input_max=%v, want 128000", tier.InputMax)
	}

	cleaned, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cleaned), "min_tokens") || strings.Contains(string(cleaned), "max_tokens") {
		t.Fatalf("legacy fields leaked into cleaned JSON: %s", cleaned)
	}
}
