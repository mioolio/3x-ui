package dispatcher

import (
	"math"
	"math/big"
	"testing"
)

func TestPanelMultiplierPreservesLargeCustomValue(t *testing.T) {
	const factor = int64(2_000_000_000)
	if got := panelMultiplier(factor); got != factor {
		t.Fatalf("custom factor was truncated: %d, want %d", got, factor)
	}
	if got := panelMultiplier(9_007_199_254_740_991); got != 9_007_199_254_740_991 {
		t.Fatalf("largest JSON-safe factor was truncated: %d", got)
	}
}

func TestPanelBillingLargeFactorUsesFullProductBeforeSaturating(t *testing.T) {
	const factor = int64(9_007_199_254_740_991)
	const physical = int64(64 * 1024)
	got, fraction := panelBillableBytesAtFactor(physical, 0, 0, factor, nil, nil)
	product := new(big.Int).Mul(big.NewInt(physical), big.NewInt(factor))
	want, remainder := new(big.Int).QuoRem(product, big.NewInt(10_000), new(big.Int))
	if got != want.Int64() || fraction != remainder.Int64() {
		t.Fatalf("charged %d + %d/10000; want %s + %s/10000", got, fraction, want, remainder)
	}
	if got, _ := panelBillableBytesAtFactor(20_000_000, 0, 0, factor, nil, nil); got != math.MaxInt64 {
		t.Fatalf("unrepresentable charge should saturate at int64 max, got %d", got)
	}
}

func TestPanelLargeFactorFindsQuotaBoundaryWithoutOverflow(t *testing.T) {
	const factor = int64(9_007_199_254_740_991)
	const remaining = int64(math.MaxInt64 / 2)
	numerator := new(big.Int).Sub(new(big.Int).Mul(big.NewInt(remaining), big.NewInt(10_000)), big.NewInt(7_000))
	want := new(big.Int).Div(new(big.Int).Add(numerator, big.NewInt(factor-1)), big.NewInt(factor))
	if got := panelBytesToQuotaBoundary(remaining, 7_000, factor); got != want.Int64() {
		t.Fatalf("quota boundary %d, want %s", got, want)
	}
}

func TestPanelOverageUses370xAfter50xInboundAllowance(t *testing.T) {
	windows := []panelQuotaRef{{rule: panelQuota{Action: "throttle", MultiplierBps: 3_700_000}, state: panelQuotaState{left: 50}}}
	charged, fraction := panelBillableBytesAtFactor(3, 0, 0, 500_000, nil, windows)
	if charged != 790 || fraction != 0 {
		t.Fatalf("first byte at 50x, next two at 370x: charged=%d fraction=%d", charged, fraction)
	}
}
