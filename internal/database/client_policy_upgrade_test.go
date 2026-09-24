package database

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// An upgrade adds policy columns in place. The old traffic counters and paid
// limits must survive, and newly introduced policy fields must be inert.
func TestClientPolicyUpgradePreservesProductionColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "old.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, statement := range []string{
		`CREATE TABLE clients (id integer primary key, email text, sub_id text, total_gb integer, expiry_time integer, enable boolean)`,
		`CREATE TABLE client_traffics (id integer primary key, email text, up integer, down integer, total integer, expiry_time integer, enable boolean)`,
		`INSERT INTO clients (id, email, sub_id, total_gb, expiry_time, enable) VALUES (17, 'legacy@example', 'old-sub', 107374182400, 1800000000000, 1)`,
		`INSERT INTO client_traffics (id, email, up, down, total, expiry_time, enable) VALUES (23, 'legacy@example', 12345, 67890, 107374182400, 1800000000000, 1)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("prepare old schema: %v", err)
		}
	}
	if err := db.AutoMigrate(&model.ClientRecord{}, &xray.ClientTraffic{}); err != nil {
		t.Fatalf("upgrade schema: %v", err)
	}
	var client model.ClientRecord
	if err := db.First(&client, 17).Error; err != nil {
		t.Fatal(err)
	}
	var traffic xray.ClientTraffic
	if err := db.First(&traffic, 23).Error; err != nil {
		t.Fatal(err)
	}
	if client.Email != "legacy@example" || client.SubID != "old-sub" || client.TotalGB != 107374182400 || client.ExpiryTime != 1800000000000 || !client.Enable {
		t.Fatalf("old client changed: %+v", client)
	}
	if traffic.Email != "legacy@example" || traffic.Up != 12345 || traffic.Down != 67890 || traffic.Total != 107374182400 || traffic.ExpiryTime != 1800000000000 || !traffic.Enable {
		t.Fatalf("old traffic changed: %+v", traffic)
	}
	if traffic.ChargeExtraBytes != 0 || client.TotalExhaustAction != "stop" || client.WindowExhaustAction != "stop" ||
		client.TotalOverageMultiplierBps != model.DefaultOverageMultiplierBps || client.WindowOverageMultiplierBps != model.DefaultOverageMultiplierBps {
		t.Fatalf("new policy defaults are not inert: client=%+v extra=%d", client, traffic.ChargeExtraBytes)
	}
}
