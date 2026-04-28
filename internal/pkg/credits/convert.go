package credits

import "math"

// CreditPerRMB is the user-facing exchange rate: 1 CNY = 10,000 credits.
const CreditPerRMB = 10000

// BillingUnitsPerRMB is the internal ledger precision: 1 CNY = 100,000,000 units.
const BillingUnitsPerRMB = 100000000

// BillingUnitsPerCredit means 1 display credit = 10,000 internal billing units.
const BillingUnitsPerCredit = BillingUnitsPerRMB / CreditPerRMB

// RMBToCredits converts RMB to display credits. Compatibility helper only.
func RMBToCredits(rmb float64) int64 {
	return int64(math.Round(rmb * CreditPerRMB))
}

// CreditsToRMB converts display credits to RMB. Compatibility helper only.
func CreditsToRMB(credits int64) float64 {
	return float64(credits) / CreditPerRMB
}

// RMBToBillingUnits converts RMB to internal ledger units.
func RMBToBillingUnits(rmb float64) int64 {
	return int64(math.Round(rmb * BillingUnitsPerRMB))
}

// BillingUnitsToRMB converts internal ledger units to RMB.
func BillingUnitsToRMB(units int64) float64 {
	return float64(units) / BillingUnitsPerRMB
}

// CreditsToBillingUnits converts display credits to internal ledger units.
func CreditsToBillingUnits(credits int64) int64 {
	return credits * BillingUnitsPerCredit
}

// BillingUnitsToCredits converts internal ledger units to rounded display credits.
// Do not use this rounded value as the billing truth source.
func BillingUnitsToCredits(units int64) int64 {
	return int64(math.Round(float64(units) / BillingUnitsPerCredit))
}

// BillingUnitsToCreditAmount returns display credits with decimals.
func BillingUnitsToCreditAmount(units int64) float64 {
	return float64(units) / BillingUnitsPerCredit
}

// MulDivRound computes round(numerator*multiplier/denominator) for positive units.
func MulDivRound(numerator int64, multiplier int64, denominator int64) int64 {
	if numerator <= 0 || multiplier <= 0 || denominator <= 0 {
		return 0
	}
	return (numerator*multiplier + denominator/2) / denominator
}

// CreditsPerMillionToBillingUnits converts a per-million-token credit price to units.
func CreditsPerMillionToBillingUnits(priceCreditsPerMillion int64) int64 {
	if priceCreditsPerMillion <= 0 {
		return 0
	}
	return CreditsToBillingUnits(priceCreditsPerMillion)
}

// CostUnitsFromCreditsPerMillion calculates token cost in billing units.
func CostUnitsFromCreditsPerMillion(priceCreditsPerMillion int64, quantity int64) int64 {
	return MulDivRound(CreditsPerMillionToBillingUnits(priceCreditsPerMillion), quantity, 1_000_000)
}

// CostUnitsFromRMBPerMillion calculates token cost in billing units from an RMB price.
func CostUnitsFromRMBPerMillion(priceRMBPerMillion float64, quantity int64) int64 {
	if priceRMBPerMillion <= 0 || quantity <= 0 {
		return 0
	}
	return MulDivRound(RMBToBillingUnits(priceRMBPerMillion), quantity, 1_000_000)
}

// CalculateWithFee calculates net RMB after payment fees.
func CalculateWithFee(amount, rate, feeRate float64) float64 {
	rmbAmount := amount * rate
	fee := rmbAmount * feeRate
	return rmbAmount - fee
}

// CalculateCreditsFromForeignCurrency converts foreign payment amount to display credits.
func CalculateCreditsFromForeignCurrency(amount, rate, feeRate float64) int64 {
	rmbNet := CalculateWithFee(amount, rate, feeRate)
	return RMBToCredits(rmbNet)
}
