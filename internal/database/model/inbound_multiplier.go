package model

const DefaultTrafficMultiplierBps = 10_000

// MaxTrafficMultiplierBps fits exactly in JavaScript's integer range, so the
// management UI and JSON API can round-trip even very large custom factors.
const MaxTrafficMultiplierBps = 9_007_199_254_740_991

// EffectiveTrafficMultiplierBps keeps legacy rows and API clients at 1x.
func EffectiveTrafficMultiplierBps(value int) int {
	if value == 0 {
		return DefaultTrafficMultiplierBps
	}
	return value
}
