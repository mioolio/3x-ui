package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestNodeDisableIsStaleWithPolicy(t *testing.T) {
	now := time.Now().UnixMilli()
	expired := now - int64(time.Hour/time.Millisecond)
	cases := []struct {
		name                           string
		master                         xray.ClientTraffic
		policy                         model.ClientRecord
		deltaUp, deltaDown, deltaExtra int64
		want                           bool
	}{
		{"slow overage beats matching old hard limit", xray.ClientTraffic{Total: 100, Up: 120}, model.ClientRecord{TotalExhaustAction: "throttle"}, 0, 0, 0, true},
		{"extra charge crosses hard total", xray.ClientTraffic{Total: 100, Up: 80, ChargeExtraBytes: 10}, model.ClientRecord{TotalExhaustAction: "stop"}, 0, 0, 11, false},
		{"grace permits old expiry", xray.ClientTraffic{ExpiryTime: expired, Up: 140, GraceBaselineExpiry: expired, GraceBaselineBytes: 100}, model.ClientRecord{GraceHours: 2, GraceQuotaBytes: 100}, 0, 0, 0, true},
		{"timed-only grace permits old expiry", xray.ClientTraffic{ExpiryTime: expired, Up: 140}, model.ClientRecord{GraceHours: 2}, 0, 0, 0, true},
		{"timed-only grace still obeys total hard stop", xray.ClientTraffic{Total: 100, ExpiryTime: expired, Up: 140}, model.ClientRecord{TotalExhaustAction: "stop", GraceHours: 2}, 0, 0, 0, false},
		{"grace crossing ends permission", xray.ClientTraffic{ExpiryTime: expired, Up: 140, GraceBaselineExpiry: expired, GraceBaselineBytes: 100}, model.ClientRecord{GraceHours: 2, GraceQuotaBytes: 100}, 60, 0, 0, false},
		{"total hard stop overrides grace", xray.ClientTraffic{Total: 100, ExpiryTime: expired, Up: 140, GraceBaselineExpiry: expired, GraceBaselineBytes: 100}, model.ClientRecord{TotalExhaustAction: "stop", GraceHours: 2, GraceQuotaBytes: 100}, 0, 0, 0, false},
		{"spent grace stays stopped", xray.ClientTraffic{ExpiryTime: expired, Up: 200, GraceBaselineExpiry: expired, GraceBaselineBytes: 100}, model.ClientRecord{GraceHours: 2, GraceQuotaBytes: 100}, 0, 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := xray.ClientTraffic{ExpiryTime: tc.master.ExpiryTime, Total: tc.master.Total, Enable: false}
			if got := nodeDisableIsStaleWithPolicy(&tc.master, node, &tc.policy, now, tc.deltaUp, tc.deltaDown, tc.deltaExtra); got != tc.want {
				t.Fatalf("stale disable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNodeOldHardStopDoesNotLatchSlowOverage(t *testing.T) {
	db := initTrafficTestDB(t)
	const email = "slow-after-old-limit"
	createNodeInboundWithClient(t, db, 1, "slow-node", 41019, email)
	svc := &InboundService{}
	initialSettings := fmt.Sprintf(`{"clients":[{"email":%q,"enable":true,"totalGB":100}]}`, email)
	syncNodeWithSettings(t, svc, 1, "slow-node", initialSettings, xray.ClientTraffic{Email: email, Total: 100, Up: 80, Enable: true})
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).
		Updates(map[string]any{"total_exhaust_action": "throttle", "total_exhaust_up_kbps": 64, "total_exhaust_down_kbps": 128}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", email).
		Updates(map[string]any{"up": 120, "enable": true}).Error; err != nil {
		t.Fatal(err)
	}
	staleSettings := fmt.Sprintf(`{"clients":[{"email":%q,"enable":false,"totalGB":100}]}`, email)
	syncNodeWithSettings(t, svc, 1, "slow-node", staleSettings, xray.ClientTraffic{Email: email, Total: 100, Up: 120, Enable: false})
	if got := readTraffic(t, db, email); !got.Enable {
		t.Fatal("old node hard stop disabled a master slow-overage client")
	}
}

func TestNodeOldExpiryDoesNotLatchActiveGrace(t *testing.T) {
	db := initTrafficTestDB(t)
	const email = "grace-after-old-expiry"
	createNodeInboundWithClient(t, db, 1, "grace-node", 41020, email)
	svc := &InboundService{}
	expired := time.Now().Add(-30 * time.Minute).UnixMilli()
	initialSettings := fmt.Sprintf(`{"clients":[{"email":%q,"enable":true,"expiryTime":%d}]}`, email, expired)
	syncNodeWithSettings(t, svc, 1, "grace-node", initialSettings, xray.ClientTraffic{Email: email, ExpiryTime: expired, Up: 100, Enable: true})
	if err := db.Model(&model.ClientRecord{}).Where("email = ?", email).
		Updates(map[string]any{"grace_hours": 2, "grace_quota_bytes": 100, "grace_up_kbps": 64, "grace_down_kbps": 128}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", email).
		Updates(map[string]any{"grace_baseline_expiry": expired, "grace_baseline_bytes": 0, "enable": true}).Error; err != nil {
		t.Fatal(err)
	}
	staleSettings := fmt.Sprintf(`{"clients":[{"email":%q,"enable":false,"expiryTime":%d}]}`, email, expired)
	syncNodeWithSettings(t, svc, 1, "grace-node", staleSettings, xray.ClientTraffic{Email: email, ExpiryTime: expired, Up: 120, Enable: false})
	if got := readTraffic(t, db, email); !got.Enable || got.Up != 20 {
		t.Fatalf("old node expiry changed active grace: enable=%v up=%d", got.Enable, got.Up)
	}
}
