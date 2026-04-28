package credits

import "testing"

func TestBillingUnitsPreserveFractionalCredits(t *testing.T) {
	units := CostUnitsFromCreditsPerMillion(3754, 10_000)
	if units != 375400 {
		t.Fatalf("expected 37.54 credits as 375400 units, got %d", units)
	}
	if got := BillingUnitsToCreditAmount(units); got != 37.54 {
		t.Fatalf("expected decimal credits 37.54, got %f", got)
	}
}

func TestTinyRequestRoundsToUnitNotWholeCredit(t *testing.T) {
	units := CostUnitsFromCreditsPerMillion(3754, 1)
	if units != 38 {
		t.Fatalf("expected 0.0038 credits as 38 units, got %d", units)
	}
	if roundedCredits := BillingUnitsToCredits(units); roundedCredits != 0 {
		t.Fatalf("legacy rounded credits must not be used as truth, got %d", roundedCredits)
	}
}
