package service

import (
	"math"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWindowQuotaStatus(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:window-quota-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ClientWindowSample{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)
	fixedStart, fixedEnd := windowBounds(2, "fixed", now)
	if fixedStart != time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC).UnixMilli() || fixedEnd != time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("wrong fixed window: %d - %d", fixedStart, fixedEnd)
	}
	rows := []model.ClientWindowSample{
		{ClientId: 1, InboundId: 0, BucketStart: now.Add(-3 * time.Hour).UnixMilli(), Bytes: 90},
		{ClientId: 1, InboundId: 0, BucketStart: now.Add(-1 * time.Hour).UnixMilli(), Bytes: 40},
		{ClientId: 1, InboundId: 0, BucketStart: now.UnixMilli(), Bytes: 30},
		{ClientId: 1, InboundId: 7, BucketStart: now.UnixMilli(), Bytes: 80},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mode         string
		inboundID          int
		wantUsed, wantLeft int64
	}{
		{"fixed", "fixed", 0, 30, 70},
		{"rolling", "rolling", 0, 70, 30},
		{"per inbound", "fixed", 7, 80, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := WindowQuotaStatus(db, 1, tc.inboundID, 100, 2, tc.mode, now)
			if err != nil {
				t.Fatal(err)
			}
			if status.UsedBytes != tc.wantUsed || status.RemainingBytes != tc.wantLeft {
				t.Fatalf("got used=%d remaining=%d", status.UsedBytes, status.RemainingBytes)
			}
		})
	}
}

func TestWindowQuotaLegacySchemaMigrates(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:window-quota-legacy-schema?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE client_window_samples (
		client_id integer, inbound_id integer, bucket_start integer, bytes integer NOT NULL,
		PRIMARY KEY (client_id, inbound_id, bucket_start))`).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	if err := db.Exec("INSERT INTO client_window_samples (client_id, inbound_id, bucket_start, bytes) VALUES (?, 0, ?, 20)", 1, now.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ClientWindowSample{}); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&model.ClientWindowSample{}, "charged_bytes") {
		t.Fatal("migration did not add charged_bytes")
	}
	status, err := WindowQuotaStatus(db, 1, 0, 100, 2, "fixed", now)
	if err != nil {
		t.Fatal(err)
	}
	if status.UsedBytes != 20 {
		t.Fatalf("legacy sample used=%d, want 20 until it ages out", status.UsedBytes)
	}
}

func TestWindowQuotaChargedTrafficAcrossInbounds(t *testing.T) {
	db := initTrafficTestDB(t)
	client := &model.ClientRecord{Email: "window-charge@example.invalid", SubID: "window-charge", Enable: true}
	if err := db.Create(client).Error; err != nil {
		t.Fatal(err)
	}
	premium := &model.Inbound{Tag: "premium-50x", Protocol: model.VMESS, Port: 28101, Enable: true, TrafficMultiplierBps: 500_000, Settings: `{}`}
	discount := &model.Inbound{Tag: "discount-001x", Protocol: model.VMESS, Port: 28102, Enable: true, TrafficMultiplierBps: 100, Settings: `{}`}
	if err := db.Create(premium).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(discount).Error; err != nil {
		t.Fatal(err)
	}
	// This pre-upgrade sample is physical-only. The new write may land in
	// the same minute, so the upsert must carry its 20 bytes forward.
	legacy := model.ClientWindowSample{ClientId: client.Id, InboundId: 0, BucketStart: time.Now().Truncate(time.Minute).UnixMilli(), Bytes: 20}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	global := []*xray.ClientTraffic{{Email: client.Email, Up: 100, Down: 50, ChargeExtraDelta: 4900, ChargeDiscountDelta: 49}}
	perInbound := []*xray.InboundClientTraffic{
		{Tag: premium.Tag, Email: client.Email, Up: 100, ChargeExtraDelta: 4900, ChargeCountersSeen: true},
		{Tag: discount.Tag, Email: client.Email, Down: 50, ChargeDiscountDelta: 49, ChargeCountersSeen: true},
	}
	if err := new(XrayService).RecordWindowTraffic(global, perInbound); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		inboundID int
		used      int64
	}{
		{0, 5021}, {premium.Id, 5000}, {discount.Id, 1},
	} {
		status, err := WindowQuotaStatus(db, client.Id, tc.inboundID, 20_000, 2, "rolling", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if status.UsedBytes != tc.used {
			t.Fatalf("window %d used %d, want %d", tc.inboundID, status.UsedBytes, tc.used)
		}
	}
	var physical, charged int64
	if err := db.Model(&model.ClientWindowSample{}).Where("client_id = ? AND inbound_id = 0", client.Id).
		Select("SUM(bytes), SUM(COALESCE(charged_bytes, bytes))").Row().Scan(&physical, &charged); err != nil {
		t.Fatal(err)
	}
	if physical != 170 || charged != 5021 {
		t.Fatalf("sample physical=%d charged=%d, want 170 and 5021", physical, charged)
	}
	// A later policy edit cannot reprice a sample already measured by Xray.
	if err := db.Model(premium).Update("traffic_multiplier_bps", 10_000).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(discount).Update("traffic_multiplier_bps", 10_000).Error; err != nil {
		t.Fatal(err)
	}
	if err := new(XrayService).RecordWindowTraffic(global, perInbound); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		inboundID int
		used      int64
	}{
		{0, 10022}, {premium.Id, 10000}, {discount.Id, 2},
	} {
		status, err := WindowQuotaStatus(db, client.Id, tc.inboundID, 20_000, 2, "rolling", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if status.UsedBytes != tc.used {
			t.Fatalf("after policy edit, window %d used %d, want %d", tc.inboundID, status.UsedBytes, tc.used)
		}
	}
}

func TestWindowQuotaExactSmallDiscountAndFallback(t *testing.T) {
	db := initTrafficTestDB(t)
	client := &model.ClientRecord{Email: "small-window@example.invalid", SubID: "small-window", Enable: true}
	if err := db.Create(client).Error; err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{Tag: "small-discount", Protocol: model.VMESS, Port: 28103, Enable: true, TrafficMultiplierBps: 100, Settings: `{}`}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	// Exact counter values carry fractional 0.01x billing across polls.
	for _, row := range []*xray.InboundClientTraffic{
		{Tag: inbound.Tag, Email: client.Email, Up: 1, ChargeDiscountDelta: 1, ChargeCountersSeen: true},
		{Tag: inbound.Tag, Email: client.Email, Up: 1, ChargeCountersSeen: true},
	} {
		if err := new(XrayService).RecordWindowTraffic(nil, []*xray.InboundClientTraffic{row}); err != nil {
			t.Fatal(err)
		}
	}
	status, err := WindowQuotaStatus(db, client.Id, inbound.Id, 100, 2, "rolling", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if status.UsedBytes != 1 {
		t.Fatalf("exact small discount used=%d, want 1", status.UsedBytes)
	}
	// A physical-only older core uses the current factor as a bounded
	// approximation. A legacy zero factor must remain 1x.
	if err := new(XrayService).RecordWindowTraffic(nil, []*xray.InboundClientTraffic{{Tag: inbound.Tag, Email: client.Email, Up: 100}}); err != nil {
		t.Fatal(err)
	}
	status, err = WindowQuotaStatus(db, client.Id, inbound.Id, 100, 2, "rolling", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if status.UsedBytes != 2 {
		t.Fatalf("fallback 0.01x used=%d, want 2", status.UsedBytes)
	}
	if err := db.Model(inbound).Update("traffic_multiplier_bps", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := new(XrayService).RecordWindowTraffic(nil, []*xray.InboundClientTraffic{{Tag: inbound.Tag, Email: client.Email, Up: 1}}); err != nil {
		t.Fatal(err)
	}
	status, err = WindowQuotaStatus(db, client.Id, inbound.Id, 100, 2, "rolling", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if status.UsedBytes != 3 {
		t.Fatalf("legacy zero factor used=%d, want 3", status.UsedBytes)
	}
}

func TestWindowQuotaLateDiscountCorrectsPriorPoll(t *testing.T) {
	db := initTrafficTestDB(t)
	client := &model.ClientRecord{Email: "late-discount@example.invalid", SubID: "late-discount", Enable: true}
	if err := db.Create(client).Error; err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{Tag: "late-discount", Protocol: model.VMESS, Port: 28104, Enable: true, Settings: `{}`}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	first := []*xray.ClientTraffic{{Email: client.Email, Up: 1}}
	firstInbound := []*xray.InboundClientTraffic{{Tag: inbound.Tag, Email: client.Email, Up: 1, ChargeCountersSeen: true}}
	if err := new(XrayService).RecordWindowTraffic(first, firstInbound); err != nil {
		t.Fatal(err)
	}
	// Stats may cross a polling boundary. This credit has no physical bytes
	// in its own poll, yet it belongs in both quota dimensions.
	credit := []*xray.ClientTraffic{{Email: client.Email, ChargeDiscountDelta: 1}}
	creditInbound := []*xray.InboundClientTraffic{{Tag: inbound.Tag, Email: client.Email, ChargeDiscountDelta: 1, ChargeCountersSeen: true}}
	if err := new(XrayService).RecordWindowTraffic(credit, creditInbound); err != nil {
		t.Fatal(err)
	}
	for _, inboundID := range []int{0, inbound.Id} {
		status, err := WindowQuotaStatus(db, client.Id, inboundID, 100, 2, "rolling", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if status.UsedBytes != 0 {
			t.Fatalf("late discount window %d used %d, want 0", inboundID, status.UsedBytes)
		}
	}
}

func TestWindowQuotaOverflowSaturates(t *testing.T) {
	if got := windowCharged(math.MaxInt64, math.MaxInt64, 0); got != math.MaxInt64 {
		t.Fatalf("charged overflow = %d", got)
	}
	if got := windowScaleFallback(math.MaxInt64, model.MaxTrafficMultiplierBps); got != math.MaxInt64 {
		t.Fatalf("scaled overflow = %d", got)
	}
	db, err := gorm.Open(sqlite.Open("file:window-quota-overflow?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ClientWindowSample{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	charged := int64(9_000_000_000_000_000_000)
	for _, bucket := range []int64{now.Add(-time.Minute).UnixMilli(), now.UnixMilli()} {
		if err := db.Create(&model.ClientWindowSample{ClientId: 1, BucketStart: bucket, Bytes: 1, ChargedBytes: &charged}).Error; err != nil {
			t.Fatal(err)
		}
	}
	status, err := WindowQuotaStatus(db, 1, 0, math.MaxInt64, 2, "rolling", now)
	if err != nil {
		t.Fatal(err)
	}
	if status.UsedBytes != math.MaxInt64 || status.RemainingBytes != 0 {
		t.Fatalf("overflowed sum used=%d remaining=%d", status.UsedBytes, status.RemainingBytes)
	}
}
