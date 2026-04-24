package credits

import "testing"

func TestBillingUnitsConversions(t *testing.T) {
	if RMBToBillingUnits(1) != BillingUnitsPerRMB {
		t.Fatalf("1 RMB should equal %d billing units", BillingUnitsPerRMB)
	}
	if CreditsToBillingUnits(1) != BillingUnitsPerCredit {
		t.Fatalf("1 credit should equal %d billing units", BillingUnitsPerCredit)
	}
	if BillingUnitsToCredits(BillingUnitsPerCredit) != 1 {
		t.Fatal("billing units should round back to 1 credit")
	}
	if got := BillingUnitsToRMB(50000000); got != 0.5 {
		t.Fatalf("BillingUnitsToRMB = %v, want 0.5", got)
	}
}
