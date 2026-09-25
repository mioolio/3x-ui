package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The inbound editor owns the inbound's settings, not a client's quota policy.
// Its protocol form used to strip the policy fields from settings.clients; a
// subsequent full SyncInbound then persisted their zero values.
func TestUpdateInboundRemarkPreservesClientPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		settings  string
		wantQuota int64
	}{
		{"omitted clients", `{"decryption":"none"}`, 1 << 30},
		{"legacy editor stripped policy", `{"decryption":"none","clients":[{"id":"client-id","email":"quota@example.test","enable":true,"totalGB":31,"subId":"quota-sub"}]}`, 1 << 30},
		{"explicit zero quota", `{"decryption":"none","clients":[{"id":"client-id","email":"quota@example.test","enable":true,"totalGB":31,"subId":"quota-sub","windowQuotaBytes":0}]}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupConflictDB(t)
			db := database.GetDB()
			client := model.Client{
				ID: "client-id", Email: "quota@example.test", SubID: "quota-sub", Enable: true,
				TotalGB: 31, SpeedLimitKbps: 1200, WindowQuotaBytes: 1 << 30,
				WindowHours: 2, WindowMode: "fixed", TrafficReset: "weekly", TrafficResetDay: 3,
				PrivateKey: "wireguard-only-secret",
			}
			settings, err := json.Marshal(map[string]any{"decryption": "none", "clients": []map[string]any{{
				"id": client.ID, "email": client.Email, "enable": true, "totalGB": client.TotalGB,
				"subId": "stale-subscription-id",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			inbound := model.Inbound{
				Tag: "quota-inbound", Remark: "old name", Enable: true, Port: 34567,
				Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`,
				Settings: string(settings), TrafficMultiplierBps: 10000,
			}
			if err := db.Create(&inbound).Error; err != nil {
				t.Fatal(err)
			}
			if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
				t.Fatal(err)
			}
			var record model.ClientRecord
			if err := db.Where("email = ?", client.Email).First(&record).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.ClientInbound{}).
				Where("client_id = ? AND inbound_id = ?", record.Id, inbound.Id).
				Updates(map[string]any{"window_quota_bytes": int64(1 << 29), "window_hours": 2,
					"window_mode": "rolling", "window_exhaust_action": "throttle",
					"window_exhaust_up_kbps": 64, "window_exhaust_down_kbps": 128,
					"window_overage_multiplier_bps": 3700000, "speed_limit_down_kbps": 3000}).Error; err != nil {
				t.Fatal(err)
			}
			charged := int64(2500)
			sample := model.ClientWindowSample{
				ClientId: record.Id, InboundId: inbound.Id, BucketStart: 60000,
				Bytes: 1000, ChargedBytes: &charged,
			}
			if err := db.Create(&sample).Error; err != nil {
				t.Fatal(err)
			}
			update := inbound
			update.Remark = "new name"
			update.Settings = tc.settings
			if _, _, err := (&InboundService{}).UpdateInbound(&update); err != nil {
				t.Fatal(err)
			}
			if err := db.Where("email = ?", client.Email).First(&record).Error; err != nil {
				t.Fatal(err)
			}
			if record.WindowQuotaBytes != tc.wantQuota || record.WindowHours != 2 ||
				record.WindowMode != "fixed" || record.SpeedLimitKbps != 1200 ||
				record.TrafficReset != "weekly" || record.TrafficResetDay != 3 ||
				record.SubID != client.SubID {
				t.Fatalf("client policy changed after rename: %+v", record)
			}
			var link model.ClientInbound
			if err := db.Where("client_id = ? AND inbound_id = ?", record.Id, inbound.Id).First(&link).Error; err != nil {
				t.Fatal(err)
			}
			if link.WindowQuotaBytes != 1<<29 || link.WindowHours != 2 ||
				link.WindowMode != "rolling" || link.WindowExhaustAction != "throttle" ||
				link.WindowOverageMultiplierBps != 3700000 ||
				link.SpeedLimitDownKbps == nil || *link.SpeedLimitDownKbps != 3000 {
				t.Fatalf("per-inbound policy changed after rename: %+v", link)
			}
			var savedSample model.ClientWindowSample
			if err := db.Where("client_id = ? AND inbound_id = ? AND bucket_start = ?", record.Id, inbound.Id, sample.BucketStart).
				First(&savedSample).Error; err != nil {
				t.Fatalf("window history disappeared after rename: %v", err)
			}
			if savedSample.Bytes != 1000 || savedSample.ChargedBytes == nil || *savedSample.ChargedBytes != charged {
				t.Fatalf("window history changed after rename: %+v", savedSample)
			}
			var saved model.Inbound
			if err := db.First(&saved, inbound.Id).Error; err != nil {
				t.Fatal(err)
			}
			if saved.Remark != "new name" {
				t.Fatalf("remark = %q", saved.Remark)
			}
			storedClients, err := ParseInboundSettingsClients(saved.Settings)
			if err != nil || len(storedClients) != 1 ||
				storedClients[0].WindowQuotaBytes != tc.wantQuota || storedClients[0].SubID != client.SubID {
				t.Fatalf("settings lost the client's quota: clients=%+v, error=%v", storedClients, err)
			}
			if storedClients[0].PrivateKey != "" {
				t.Fatal("a VLESS inbound received an unrelated WireGuard private key")
			}
		})
	}
}

func TestUpdateInboundRemarkDoesNotReattachDetachedClient(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()
	inbound := model.Inbound{
		Tag: "detached-inbound", Remark: "old name", Enable: true, Port: 34568,
		Protocol: model.VLESS, StreamSettings: `{"network":"tcp"}`,
		Settings:             `{"decryption":"none","clients":[{"id":"old-id","email":"detached@example.test","enable":true}]}`,
		TrafficMultiplierBps: 10000,
	}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	client := model.Client{ID: "old-id", Email: "detached@example.test", Enable: true}
	cs := &ClientService{}
	if err := cs.SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	// Simulate a detach committed by the client editor while the older
	// embedded settings JSON has not been reconciled yet.
	if err := cs.DetachInbound(nil, inbound.Id); err != nil {
		t.Fatal(err)
	}
	update := inbound
	update.Remark = "new name"
	update.Settings = `{"decryption":"none"}`
	if _, _, err := (&InboundService{}).UpdateInbound(&update); err != nil {
		t.Fatal(err)
	}
	var links int64
	if err := db.Model(&model.ClientInbound{}).Where("inbound_id = ?", inbound.Id).Count(&links).Error; err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("rename reattached %d deliberately detached client(s)", links)
	}
	var saved model.Inbound
	if err := db.First(&saved, inbound.Id).Error; err != nil {
		t.Fatal(err)
	}
	clients, err := ParseInboundSettingsClients(saved.Settings)
	if err != nil || len(clients) != 0 {
		t.Fatalf("stale client survived in stored settings: %+v, error=%v", clients, err)
	}
}
