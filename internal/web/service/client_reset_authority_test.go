package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientResetRejectsFreshMasterUsageBeforeMutating(t *testing.T) {
	db := initTrafficTestDB(t)
	const email = "managed@example.com"
	client := &model.ClientRecord{Email: email, SubID: "managed", Enable: true}
	if err := db.Create(client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(client).UpdateColumn("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	traffic := &xray.ClientTraffic{Email: email, Enable: true, Total: 310 << 30, Up: 17, Down: 23}
	if err := db.Create(traffic).Error; err != nil {
		t.Fatal(err)
	}
	global := &model.ClientGlobalTraffic{MasterGuid: "parent", Email: email, Up: 719080279292, Down: 132410702250}
	if err := db.Create(global).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := (&ClientService{}).ResetTrafficByEmail(&InboundService{}, email); err == nil || !strings.Contains(err.Error(), "主面板重置") {
		t.Fatalf("local reset should explain master authority, got %v", err)
	}
	var gotClient model.ClientRecord
	if err := db.First(&gotClient, client.Id).Error; err != nil {
		t.Fatal(err)
	}
	if gotClient.Enable {
		t.Fatal("rejected reset auto-enabled the client")
	}
	gotTraffic := readTraffic(t, db, email)
	if gotTraffic.Up != 17 || gotTraffic.Down != 23 {
		t.Fatalf("rejected reset changed local usage: %d/%d", gotTraffic.Up, gotTraffic.Down)
	}
	var gotGlobal model.ClientGlobalTraffic
	if err := db.Where("email = ?", email).First(&gotGlobal).Error; err != nil {
		t.Fatal(err)
	}
	if gotGlobal.Up != global.Up || gotGlobal.Down != global.Down {
		t.Fatalf("rejected reset changed master usage: %d/%d", gotGlobal.Up, gotGlobal.Down)
	}
}

func TestClientResetFromMasterBypassesFreshGlobalGuard(t *testing.T) {
	db := initTrafficTestDB(t)
	startSerializedWriter(t)
	const email = "managed@example.com"
	if err := db.Create(&model.ClientRecord{Email: email, SubID: "managed", Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: email, Enable: true, Up: 17, Down: 23}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientGlobalTraffic{MasterGuid: "parent", Email: email, Up: 719080279292, Down: 132410702250}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetTrafficByEmailFromMaster(&InboundService{}, email); err != nil {
		t.Fatalf("upstream reset must reach the local counter: %v", err)
	}
	if got := readTraffic(t, db, email); got.Up != 0 || got.Down != 0 {
		t.Fatalf("local usage after upstream reset: %d/%d", got.Up, got.Down)
	}
	var globals int64
	if err := db.Model(&model.ClientGlobalTraffic{}).Where("email = ?", email).Count(&globals).Error; err != nil {
		t.Fatal(err)
	}
	if globals != 0 {
		t.Fatal("upstream reset left stale master overlay")
	}
}

func TestClientResetOnMasterIgnoresOwnChildGlobalUsage(t *testing.T) {
	db := initTrafficTestDB(t)
	startSerializedWriter(t)
	const email = "master@example.com"
	if err := db.Create(&model.Node{Name: "registered-child", Guid: "child-guid", Address: "127.0.0.1", Port: 2096, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientRecord{Email: email, SubID: "master", Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: email, Enable: true, Up: 17, Down: 23}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientGlobalTraffic{MasterGuid: "child-guid", Email: email, Up: 719080279292, Down: 132410702250}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetTrafficByEmail(&InboundService{}, email); err != nil {
		t.Fatalf("a child panel's stale global row must not prevent its master from resetting: %v", err)
	}
	if got := readTraffic(t, db, email); got.Up != 0 || got.Down != 0 {
		t.Fatalf("master usage after reset: %d/%d", got.Up, got.Down)
	}
}

func TestResetClientTrafficReportsRemoteFailure(t *testing.T) {
	db := initTrafficTestDB(t)
	startSerializedWriter(t)
	nodeID, remote := setupNodeRuntime(t)
	const email = "remote-reset@example.com"
	ib := nodeInbound(t, nodeID, 45491, []model.Client{{Email: email, ID: "11111111-1111-1111-1111-111111111111", Enable: true}})
	if err := db.Create(&xray.ClientTraffic{InboundId: ib.Id, Email: email, Enable: true, Up: 60, Down: 40}).Error; err != nil {
		t.Fatal(err)
	}
	remote.resetErr = errors.New("remote unavailable")
	if _, err := (&InboundService{}).ResetClientTraffic(ib.Id, email); err == nil || !strings.Contains(err.Error(), "remote unavailable") {
		t.Fatalf("remote reset failure must reach API caller, got %v", err)
	}
	if remote.resetCalls.Load() != 1 {
		t.Fatalf("remote reset calls = %d, want 1", remote.resetCalls.Load())
	}
	if got := readTraffic(t, db, email); got.Up != 0 || got.Down != 0 {
		t.Fatalf("local reset should remain committed for safe retry: %d/%d", got.Up, got.Down)
	}
}

func TestClientResetIgnoresStaleMasterUsage(t *testing.T) {
	db := initTrafficTestDB(t)
	startSerializedWriter(t)
	const email = "standalone@example.com"
	if err := db.Create(&model.ClientRecord{Email: email, SubID: "standalone", Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&xray.ClientTraffic{Email: email, Enable: true, Up: 17, Down: 23}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientGlobalTraffic{MasterGuid: "old-parent", Email: email, Up: 99, UpdatedAt: time.Now().Add(-globalTrafficFreshWindow - time.Minute).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClientService{}).ResetTrafficByEmail(&InboundService{}, email); err != nil {
		t.Fatalf("stale master should not block local reset: %v", err)
	}
	if got := readTraffic(t, db, email); got.Up != 0 || got.Down != 0 {
		t.Fatalf("local usage after reset: %d/%d", got.Up, got.Down)
	}
}
