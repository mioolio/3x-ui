package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestGroupSummaryUsesBilledDirectionsAndResetBaseline(t *testing.T) {
	db := initTrafficTestDB(t)
	if err := db.Create(&model.ClientRecord{Email: "group-billed@x", Group: "premium", Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{
		Email: "group-billed@x", Up: 100, Down: 200,
		ChargeExtraBytes: 9_800, ChargeExtraDownBytes: 9_800,
	}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &ClientService{}
	groups, err := svc.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Up != 100 || groups[0].Down != 10_000 || groups[0].TrafficUsed != 10_100 {
		t.Fatalf("initial billed group = %+v", groups)
	}
	if err := svc.ResetGroupTraffic("premium"); err != nil {
		t.Fatal(err)
	}
	groups, err = svc.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].TrafficUsed != 0 {
		t.Fatalf("reset group = %+v", groups)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", "group-billed@x").Updates(map[string]any{
		"down": 210, "charge_extra_bytes": 10_290, "charge_extra_down_bytes": 10_290,
	}).Error; err != nil {
		t.Fatal(err)
	}
	groups, err = svc.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Up != 0 || groups[0].Down != 500 || groups[0].TrafficUsed != 500 {
		t.Fatalf("post-reset billed group = %+v", groups)
	}
}
