package database

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestDirectionalChargeBackfillPreservesQuotaAndRestart(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	path := filepath.Join(dbDir, "x-ui.db")
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseDB() })
	if err := db.Create(&xray.ClientTraffic{Email: "old@x", Up: 100, Down: 900, ChargeExtraBytes: 49_000, Total: 1_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: "saturated@x", Up: 1, Down: 1,
		ChargeExtraBytes: TrafficMax, ChargeExtraUpBytes: TrafficMax, ChargeExtraDownBytes: TrafficMax}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeClientTraffic{NodeId: 1, Email: "old@x", Up: 100, Down: 900, ChargeExtraBytes: 49_000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientGlobalTraffic{MasterGuid: "master", Email: "old@x", Down: 100, ChargeDiscountBytes: 99}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := backfillDirectionalCharges(); err != nil {
			t.Fatal(err)
		}
		var local xray.ClientTraffic
		if err := db.Where("email = ?", "old@x").First(&local).Error; err != nil {
			t.Fatal(err)
		}
		if local.ChargeExtraUpBytes != 4_900 || local.ChargeExtraDownBytes != 44_100 {
			t.Fatalf("backfill run %d allocated %+v", i, local)
		}
		up, down := local.BilledUsage()
		if up != 5_000 || down != 45_000 || up+down != 50_000 {
			t.Fatalf("backfill run %d billed=%d/%d", i, up, down)
		}
		var node model.NodeClientTraffic
		if err := db.Where("email = ?", "old@x").First(&node).Error; err != nil {
			t.Fatal(err)
		}
		if node.ChargeExtraUpBytes != 4_900 || node.ChargeExtraDownBytes != 44_100 {
			t.Fatalf("node allocation %+v", node)
		}
		var global model.ClientGlobalTraffic
		if err := db.Where("email = ?", "old@x").First(&global).Error; err != nil {
			t.Fatal(err)
		}
		if global.ChargeDiscountUpBytes != 0 || global.ChargeDiscountDownBytes != 99 {
			t.Fatalf("global allocation %+v", global)
		}
		var saturated xray.ClientTraffic
		if err := db.Where("email = ?", "saturated@x").First(&saturated).Error; err != nil {
			t.Fatal(err)
		}
		if saturated.ChargeExtraUpBytes+saturated.ChargeExtraDownBytes != TrafficMax {
			t.Fatalf("saturated row changed aggregate bill: %+v", saturated)
		}
	}
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(path); err != nil {
		t.Fatal(err)
	}
	var after xray.ClientTraffic
	if err := db.Where("email = ?", "old@x").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.ChargeExtraUpBytes != 4_900 || after.ChargeExtraDownBytes != 44_100 {
		t.Fatalf("restart changed allocation %+v", after)
	}
}

func TestRemainingLegacyChargeSaturatedCounters(t *testing.T) {
	if got := remainingLegacyCharge(math.MaxInt64, math.MaxInt64, math.MaxInt64); got != 0 {
		t.Fatalf("remaining = %d, want zero", got)
	}
	up, down := reconcileDirectionalCharge(100, math.MaxInt64, math.MaxInt64, 1, 1)
	if up+down != 100 {
		t.Fatalf("malformed historical directions %d/%d changed aggregate quota", up, down)
	}
}
