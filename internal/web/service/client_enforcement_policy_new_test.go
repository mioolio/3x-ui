package service

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

func TestTimedExpiryGraceWithoutExtraQuotaAndDiscountAccounting(t *testing.T) {
	db := initTrafficTestDB(t)
	now := time.UnixMilli(1_800_000_000_000)
	expiry := now.Add(-time.Hour).UnixMilli()
	client := model.ClientRecord{
		Email: "timed-grace@example.invalid", SubID: "timed-grace", Enable: true,
		ExpiryTime: expiry, GraceHours: 2, GraceUpKbps: 64, GraceDownKbps: 128,
		TotalGB: 100,
	}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	traffic := xray.ClientTraffic{Email: client.Email, Enable: true, ExpiryTime: expiry, Total: 100,
		Up: 80, Down: 40, ChargeDiscountBytes: 60}
	if err := db.Create(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	total, _, grace, err := BuildEnforcementPolicy(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if total[client.Email].Remaining != 40 {
		t.Fatalf("discounted allowance left = %d, want 40", total[client.Email].Remaining)
	}
	if grace[client.Email].Remaining != math.MaxInt64 || grace[client.Email].EndsAt != expiry+int64(2*time.Hour/time.Millisecond) {
		t.Fatalf("timed grace = %+v", grace[client.Email])
	}
	if err := validateClientPolicy(model.Client{GraceHours: &client.GraceHours, GraceUpKbps: &client.GraceUpKbps, GraceDownKbps: &client.GraceDownKbps}, nil); err != nil {
		t.Fatalf("timed low-speed grace without an additional quota: %v", err)
	}
}

func TestPolicyUsedBytesNeverNegativeAfterDiscount(t *testing.T) {
	physical, charged := policyUsedBytes(&xray.ClientTraffic{Up: 10, ChargeDiscountBytes: 50})
	if physical != 10 || charged != 0 {
		t.Fatalf("physical=%d charged=%d, want 10 and 0", physical, charged)
	}
}

func TestRefreshRatePolicyPublishesInboundMultipliers(t *testing.T) {
	db := initTrafficTestDB(t)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	for _, inbound := range []model.Inbound{
		{Tag: "discount-node", Enable: true, Protocol: model.VLESS, TrafficMultiplierBps: 100},
		{Tag: "premium-node", Enable: true, Protocol: model.VLESS, TrafficMultiplierBps: 20_000},
		{Tag: "maximum-node", Enable: true, Protocol: model.VLESS, TrafficMultiplierBps: model.MaxTrafficMultiplierBps},
		{Tag: "normal-node", Enable: true, Protocol: model.VLESS, TrafficMultiplierBps: 10_000},
	} {
		if err := db.Create(&inbound).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := new(XrayService).RefreshRatePolicy(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(xray.GetRatePolicyPath())
	if err != nil {
		t.Fatal(err)
	}
	var policy ratePolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.InboundMultipliers["discount-node"] != 100 || policy.InboundMultipliers["premium-node"] != 20_000 || policy.InboundMultipliers["maximum-node"] != model.MaxTrafficMultiplierBps {
		t.Fatalf("published inbound multipliers = %+v", policy.InboundMultipliers)
	}
	if _, found := policy.InboundMultipliers["normal-node"]; found {
		t.Fatal("the default 1x inbound should not need a special core rule")
	}
}

func TestTrafficPollStoresPhysicalAndDiscountSeparately(t *testing.T) {
	db := initTrafficTestDB(t)
	const email = "metered@example.invalid"
	if err := db.Create(&xray.ClientTraffic{Email: email, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	svc := new(InboundService)
	if err := db.Transaction(func(tx *gorm.DB) error {
		return svc.addClientTraffic(tx, []*xray.ClientTraffic{{Email: email, Up: 100, ChargeDiscountDelta: 99}})
	}); err != nil {
		t.Fatal(err)
	}
	var got xray.ClientTraffic
	if err := db.Where("email = ?", email).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.Up != 100 || got.ChargeDiscountBytes != 99 || got.ChargeExtraBytes != 0 {
		t.Fatalf("traffic poll persisted %+v", got)
	}
	if _, charged := policyUsedBytes(&got); charged != 1 {
		t.Fatalf("charged usage = %d, want 1", charged)
	}
}

func TestTuicRejectsUnaccountableInboundMultiplier(t *testing.T) {
	inbound := &model.Inbound{Protocol: model.TUIC, TrafficMultiplierBps: 20_000}
	if _, _, err := new(InboundService).AddInbound(inbound); err == nil {
		t.Fatal("TUIC cannot apply a 2x per-client debit to encrypted aggregate UDP traffic")
	}
}
