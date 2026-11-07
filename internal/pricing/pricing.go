// Package pricing computes seat prices. Money is always integer cents to avoid float drift.
package pricing

const premiumMultiplierPct = 150

// SeatPrice returns the price of a seat given the showtime base price.
func SeatPrice(baseCents int, seatType string) int {
	if seatType == "premium" {
		return baseCents * premiumMultiplierPct / 100
	}
	return baseCents
}
