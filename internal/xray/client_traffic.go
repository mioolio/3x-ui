package xray

import (
	"encoding/json"
	"math"
	"math/big"
)

// InboundClientTraffic is the traffic delta for one authenticated client on
// one inbound, emitted by the bundled Xray dispatcher.
type InboundClientTraffic struct {
	Tag   string
	Email string
	Up    int64
	Down  int64
	// Charge deltas come from counters scoped to this inbound and client. A
	// present zero counter still matters: it proves the running core reported
	// the exact charge, so callers must not recalculate it using today's rate.
	ChargeExtraDelta    int64
	ChargeDiscountDelta int64
	ChargeCountersSeen  bool
}

// ClientTraffic represents traffic statistics and limits for a specific client.
// It tracks upload/download usage, expiry times, and online status for inbound clients.
type ClientTraffic struct {
	Id        int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement" example:"14825"`
	InboundId int    `json:"inboundId" form:"inboundId" gorm:"index:idx_client_traffics_inbound" example:"1"`
	Enable    bool   `json:"enable" form:"enable" example:"true"`
	Email     string `json:"email" form:"email" gorm:"unique" example:"user1"`
	UUID      string `json:"uuid" form:"uuid" gorm:"-" example:"e18c9a96-71bf-48d4-933f-8b9a46d4290c"`
	SubId     string `json:"subId" form:"subId" gorm:"-" example:"i7tvdpeffi0hvvf1"`
	Up        int64  `json:"up" form:"up" example:"1048576"`
	Down      int64  `json:"down" form:"down" example:"2097152"`
	// ChargeExtraBytes is the additional quota debit from configured overage
	// multipliers. Up/Down remain the physical transfer counters everywhere.
	ChargeExtraBytes int64 `json:"chargeExtraBytes" gorm:"column:charge_extra_bytes;default:0"`
	// ChargeDiscountBytes is a nonnegative cumulative allowance credit from
	// an inbound whose traffic multiplier is below 1x.
	ChargeDiscountBytes int64 `json:"chargeDiscountBytes" gorm:"column:charge_discount_bytes;default:0"`
	// Directional charge counters preserve the exact uplink/downlink bill from
	// new samples. Older aggregate charges are allocated proportionally once
	// during database migration; their original direction cannot be recovered.
	ChargeExtraUpBytes      int64 `json:"chargeExtraUpBytes" gorm:"column:charge_extra_up_bytes;default:0"`
	ChargeExtraDownBytes    int64 `json:"chargeExtraDownBytes" gorm:"column:charge_extra_down_bytes;default:0"`
	ChargeDiscountUpBytes   int64 `json:"chargeDiscountUpBytes" gorm:"column:charge_discount_up_bytes;default:0"`
	ChargeDiscountDownBytes int64 `json:"chargeDiscountDownBytes" gorm:"column:charge_discount_down_bytes;default:0"`
	// ChargeExtraDelta is populated only by the Xray stats poll. It is never
	// stored directly; AddTraffic accumulates it into ChargeExtraBytes.
	ChargeExtraDelta        int64 `json:"-" gorm:"-"`
	ChargeDiscountDelta     int64 `json:"-" gorm:"-"`
	ChargeExtraUpDelta      int64 `json:"-" gorm:"-"`
	ChargeExtraDownDelta    int64 `json:"-" gorm:"-"`
	ChargeDiscountUpDelta   int64 `json:"-" gorm:"-"`
	ChargeDiscountDownDelta int64 `json:"-" gorm:"-"`
	// A grace allowance starts at the paid expiry. The captured physical usage
	// is durable so a panel restart cannot replenish the grace allowance.
	GraceBaselineBytes  int64 `json:"-" gorm:"column:grace_baseline_bytes;default:0"`
	GraceBaselineExpiry int64 `json:"-" gorm:"column:grace_baseline_expiry;default:0"`
	QuotaEpoch          int64 `json:"-" gorm:"column:quota_epoch;default:0"`
	ExpiryTime          int64 `json:"expiryTime" form:"expiryTime" gorm:"index:idx_client_traffics_renew,priority:1" example:"1735689600000"`
	Total               int64 `json:"total" form:"total" example:"10737418240"`
	Reset               int   `json:"reset" form:"reset" gorm:"default:0;index:idx_client_traffics_renew,priority:2" example:"0"`
	// ResetDay renews on that day of each calendar month instead of every
	// Reset days; 0 keeps the interval behaviour.
	ResetDay int `json:"resetDay" form:"resetDay" gorm:"default:0" example:"0"`
	// ResetMax caps how many times auto-renew may fire; 0 means no cap.
	ResetMax int `json:"resetMax" form:"resetMax" gorm:"default:0" example:"0"`
	// ResetCount is how many have fired, so a prepaid plan stops on its own.
	ResetCount   int   `json:"resetCount" form:"resetCount" gorm:"default:0" example:"0"`
	LastOnline   int64 `json:"lastOnline" form:"lastOnline" gorm:"default:0" example:"1735680000000"`
	LastSubFetch int64 `json:"lastSubFetch" form:"lastSubFetch" gorm:"default:0" example:"1735680000000"`
}

// AllocateLegacyCharge assigns an old undirected charge by its physical
// uplink share. big.Int avoids overflow when a high multiplier makes the
// charge much larger than the physical traffic. The downlink gets the exact
// remainder so migration never changes the amount deducted from the quota.
func AllocateLegacyCharge(charge, up, down int64) (int64, int64) {
	charge, up, down = max(charge, 0), max(up, 0), max(down, 0)
	if charge == 0 {
		return 0, 0
	}
	if up == 0 && down == 0 {
		return 0, charge
	}
	numerator := new(big.Int).Mul(big.NewInt(charge), big.NewInt(up))
	denominator := new(big.Int).Add(big.NewInt(up), big.NewInt(down))
	upCharge := new(big.Int).Quo(numerator, denominator).Int64()
	return upCharge, charge - upCharge
}

func saturatingTrafficAdd(a, b int64) int64 {
	a, b = max(a, 0), max(b, 0)
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func unallocatedCharge(total, known int64) int64 {
	total, known = max(total, 0), max(known, 0)
	if total <= known {
		return 0
	}
	return total - known
}

// BilledUsage returns quota-valued upload and download. A legacy row with
// aggregate-only charges uses proportional allocation until its migration or
// first write. The final adjustment preserves the existing total charge even
// for malformed or saturated historical counters.
func (t ClientTraffic) BilledUsage() (int64, int64) {
	physicalUp, physicalDown := max(t.Up, 0), max(t.Down, 0)
	total := saturatingTrafficAdd(physicalUp, physicalDown)
	total = saturatingTrafficAdd(total, t.ChargeExtraBytes)
	total = max(0, total-max(t.ChargeDiscountBytes, 0))
	knownExtra := saturatingTrafficAdd(t.ChargeExtraUpBytes, t.ChargeExtraDownBytes)
	knownDiscount := saturatingTrafficAdd(t.ChargeDiscountUpBytes, t.ChargeDiscountDownBytes)
	legacyExtraUp, _ := AllocateLegacyCharge(unallocatedCharge(t.ChargeExtraBytes, knownExtra), physicalUp, physicalDown)
	legacyDiscountUp, _ := AllocateLegacyCharge(unallocatedCharge(t.ChargeDiscountBytes, knownDiscount), physicalUp, physicalDown)
	up := saturatingTrafficAdd(physicalUp, saturatingTrafficAdd(t.ChargeExtraUpBytes, legacyExtraUp))
	up = max(0, up-saturatingTrafficAdd(t.ChargeDiscountUpBytes, legacyDiscountUp))
	up = min(up, total)
	return up, total - up
}

// MarshalJSON adds the account-facing billed directions while retaining the
// physical counters for diagnostics and speed/capacity monitoring. Computing
// at serialization time also handles global traffic overlays correctly.
func (t ClientTraffic) MarshalJSON() ([]byte, error) {
	type alias ClientTraffic
	up, down := t.BilledUsage()
	return json.Marshal(struct {
		alias
		BilledUp   int64 `json:"billedUp"`
		BilledDown int64 `json:"billedDown"`
	}{alias(t), up, down})
}
