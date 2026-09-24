package xray

import (
	"encoding/json"
	"math"
	"testing"
)

func TestClientTrafficBilledDirections(t *testing.T) {
	for _, tc := range []struct {
		name             string
		row              ClientTraffic
		wantUp, wantDown int64
	}{
		{"50x uplink", ClientTraffic{Up: 100, Down: 50, ChargeExtraBytes: 4900, ChargeExtraUpBytes: 4900}, 5000, 50},
		{"0.01x downlink", ClientTraffic{Up: 100, Down: 100, ChargeDiscountBytes: 99, ChargeDiscountDownBytes: 99}, 100, 1},
		{"legacy extra", ClientTraffic{Up: 20, Down: 80, ChargeExtraBytes: 900}, 200, 800},
		{"legacy discount", ClientTraffic{Up: 20, Down: 80, ChargeDiscountBytes: 99}, 1, 0},
		{"zero physical legacy", ClientTraffic{ChargeExtraBytes: 50}, 0, 50},
		{"saturated", ClientTraffic{Up: math.MaxInt64, Down: math.MaxInt64, ChargeExtraBytes: math.MaxInt64}, math.MaxInt64, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up, down := tc.row.BilledUsage()
			if up != tc.wantUp || down != tc.wantDown {
				t.Fatalf("billed=%d/%d want=%d/%d", up, down, tc.wantUp, tc.wantDown)
			}
			var wire struct{ BilledUp, BilledDown int64 }
			data, err := json.Marshal(tc.row)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.BilledUp != up || wire.BilledDown != down {
				t.Fatalf("JSON billed=%d/%d, calculation=%d/%d", wire.BilledUp, wire.BilledDown, up, down)
			}
		})
	}
}

func TestAllocateLegacyChargeOverflowAndConservation(t *testing.T) {
	up, down := AllocateLegacyCharge(math.MaxInt64, math.MaxInt64, math.MaxInt64)
	if up != math.MaxInt64/2 || up+down != math.MaxInt64 {
		t.Fatalf("allocation %d/%d does not preserve total", up, down)
	}
}
