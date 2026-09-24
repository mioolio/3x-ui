package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func directionalPtr(value int64) *int64 { return &value }

func TestDirectionalRatesReachBundledPolicy(t *testing.T) {
	setupBulkDB(t)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	db := database.GetDB()
	ib := &model.Inbound{Tag: "directional-inbound", Enable: true, SpeedLimitKbps: 300,
		SpeedLimitUpKbps: directionalPtr(0), SpeedLimitDownKbps: directionalPtr(600)}
	if err := db.Create(ib).Error; err != nil {
		t.Fatal(err)
	}
	disabled := &model.Inbound{Tag: "disabled-inbound", Enable: false, SpeedLimitKbps: 900}
	if err := db.Create(disabled).Error; err != nil {
		t.Fatal(err)
	}
	client := &model.ClientRecord{Email: "directional@example.test", Enable: true,
		SpeedLimitKbps: 250, SpeedLimitUpKbps: directionalPtr(125), SpeedLimitDownKbps: directionalPtr(0)}
	if err := db.Create(client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: ib.Id, SpeedLimitKbps: 100}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &ClientService{}
	if err := svc.SetInboundDirectionalRates(client.Email, map[int]InboundDirectionalRate{
		ib.Id: {UpKbps: 75, DownKbps: 0},
	}); err != nil {
		t.Fatal(err)
	}
	rates, err := svc.GetInboundDirectionalRates(client.Email)
	if err != nil || rates[ib.Id] != (InboundDirectionalRate{UpKbps: 75, DownKbps: 0}) {
		t.Fatalf("round trip rates=%v, err=%v", rates, err)
	}
	if err := (&XrayService{}).RefreshRatePolicy(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Clean(xray.GetRatePolicyPath()))
	if err != nil {
		t.Fatal(err)
	}
	var policy ratePolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.InboundDirections[ib.Tag] != (directionalRate{UpKbps: 0, DownKbps: 600}) {
		t.Fatalf("inbound directions: %v", policy.InboundDirections)
	}
	if _, exists := policy.Inbounds[disabled.Tag]; exists {
		t.Fatal("disabled inbound received a rate policy")
	}
	if policy.ClientDirections[client.Email] != (directionalRate{UpKbps: 125, DownKbps: 0}) {
		t.Fatalf("client directions: %v", policy.ClientDirections)
	}
	key := ib.Tag + "\x00" + client.Email
	if policy.OverrideDirections[key] != (directionalRate{UpKbps: 75, DownKbps: 0}) {
		t.Fatalf("linked inbound directions: %v", policy.OverrideDirections)
	}
	links, err := loadInboundLinkPolicies(db, ib.Id)
	if err != nil || len(links) != 1 || links[0].Email != client.Email {
		t.Fatalf("link policy snapshot=%v, err=%v", links, err)
	}
	links[0].SpeedLimitUpKbps = nil
	links[0].SpeedLimitDownKbps = nil
	links[0].SpeedLimitKbps = 0
	links[0].WindowQuotaBytes = 1024
	links[0].WindowHours = 2
	links[0].WindowMode = "fixed"
	links[0].WindowExhaustAction = "throttle"
	links[0].WindowExhaustUpKbps = 64
	links[0].WindowExhaustDownKbps = 128
	links[0].WindowOverageMultiplierBps = 20000
	if err := svc.SetInboundLinkPolicies(ib.Id, links); err != nil {
		t.Fatal(err)
	}
	var saved model.ClientInbound
	if err := db.Where("client_id = ? AND inbound_id = ?", client.Id, ib.Id).First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.SpeedLimitUpKbps != nil || saved.SpeedLimitDownKbps != nil || saved.SpeedLimitKbps != 0 ||
		saved.WindowExhaustAction != "throttle" || saved.WindowOverageMultiplierBps != 20000 {
		t.Fatalf("reconciled link policy was not applied: %+v", saved)
	}
}

func TestLegacyClientRateEditReplacesDirectionalRate(t *testing.T) {
	row := &model.ClientRecord{SpeedLimitKbps: 0,
		SpeedLimitUpKbps: directionalPtr(100), SpeedLimitDownKbps: directionalPtr(200)}
	applyClientRecordMerge(row, &model.ClientRecord{SpeedLimitKbps: 300})
	if row.SpeedLimitKbps != 300 || row.SpeedLimitUpKbps != nil || row.SpeedLimitDownKbps != nil {
		t.Fatalf("legacy edit did not replace directional speed: %+v", row)
	}
	row.SpeedLimitUpKbps = directionalPtr(100)
	row.SpeedLimitDownKbps = directionalPtr(200)
	applyClientRecordMerge(row, &model.ClientRecord{SpeedLimitKbps: 300})
	if row.SpeedLimitUpKbps == nil || row.SpeedLimitDownKbps == nil {
		t.Fatal("unrelated legacy edit cleared directional speed")
	}
}
