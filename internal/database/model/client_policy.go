package model

// ClientPolicyPatch records which optional policy fields were present in a
// client payload. Legacy API clients and older inbound settings omit these
// fields; their edits must not erase a policy configured by a newer client.
type ClientPolicyPatch struct {
	TotalExhaustAction         *string
	TotalExhaustUpKbps         *int64
	TotalExhaustDownKbps       *int64
	TotalOverageMultiplierBps  *int
	WindowExhaustAction        *string
	WindowExhaustUpKbps        *int64
	WindowExhaustDownKbps      *int64
	WindowOverageMultiplierBps *int
	GraceHours                 *int
	GraceUpKbps                *int64
	GraceDownKbps              *int64
	GraceQuotaBytes            *int64
}

const DefaultOverageMultiplierBps = 10000

// JSON clients represent multiplier basis points as JavaScript numbers. Keep
// the same exact round-trip bound as an inbound multiplier.
const MaxOverageMultiplierBps = MaxTrafficMultiplierBps

func EffectiveOverageMultiplierBps(value int) int {
	if value <= 0 {
		return DefaultOverageMultiplierBps
	}
	return value
}

func policyPtr[T any](value T) *T { return &value }

func policyValue[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}
