package service

import (
	"reflect"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/mtproto"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestDesiredMtprotoInstancesFiltersDepleted(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}

	seedInboundConflict(t, "mt-desired", "", 46001, model.MTProto,
		"",
		`{"clients":[`+
			`{"email":"alice","secret":"`+mtprotoTestSecretA+`","enable":true},`+
			`{"email":"bob","secret":"`+mtprotoTestSecretB+`","enable":true},`+
			`{"email":"carol","secret":"`+mtprotoTestSecretC+`","enable":false}]}`)
	served := loadInboundByTag(t, "mt-desired")
	seedClientTraffic(t, served.Id, "alice", true)
	seedClientTraffic(t, served.Id, "bob", false)
	seedClientTraffic(t, served.Id, "carol", true)

	seedInboundConflict(t, "mt-all-depleted", "", 46002, model.MTProto,
		"",
		`{"clients":[{"email":"dave","secret":"`+mtprotoTestSecretA+`","enable":true}]}`)
	depleted := loadInboundByTag(t, "mt-all-depleted")
	seedClientTraffic(t, depleted.Id, "dave", false)

	nodeID := 5
	seedInboundConflictNode(t, "mt-node-owned", "", 46003, model.MTProto,
		"",
		`{"clients":[{"email":"erin","secret":"`+mtprotoTestSecretB+`","enable":true}]}`,
		&nodeID)

	instances, err := svc.DesiredMtprotoInstances()
	if err != nil {
		t.Fatalf("DesiredMtprotoInstances: %v", err)
	}

	t.Run("depletedAndDisabledClientsExcluded", func(t *testing.T) {
		if len(instances) != 1 {
			t.Fatalf("expected exactly the served inbound, got %d instances: %+v", len(instances), instances)
		}
		if instances[0].Id != served.Id {
			t.Fatalf("expected inbound %d, got %d", served.Id, instances[0].Id)
		}
		want := []mtproto.SecretEntry{{Name: "alice", Secret: mtprotoTestSecretA}}
		if !reflect.DeepEqual(instances[0].Secrets, want) {
			t.Fatalf("served secrets: got %+v, want %+v", instances[0].Secrets, want)
		}
	})

	t.Run("matchesInteractivePushFiltering", func(t *testing.T) {
		built, err := svc.buildInboundForLocalRuntime(database.GetDB(), served)
		if err != nil {
			t.Fatalf("buildInboundForLocalRuntime: %v", err)
		}
		pushInst, ok := mtproto.InstanceFromInbound(built)
		if !ok {
			t.Fatal("push path must produce an instance")
		}
		if !reflect.DeepEqual(pushInst.Secrets, instances[0].Secrets) {
			t.Fatalf("push and job secret sets diverge: push %+v, job %+v", pushInst.Secrets, instances[0].Secrets)
		}
	})
}

func TestDesiredMtprotoInstancesStopsSharedDisabledAndExhaustedWindows(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}
	seedInboundConflict(t, "mt-windows", "", 46101, model.MTProto, "",
		`{"clients":[`+
			`{"email":"alice","secret":"`+mtprotoTestSecretA+`","enable":true},`+
			`{"email":"bob","secret":"`+mtprotoTestSecretB+`","enable":true},`+
			`{"email":"carol","secret":"`+mtprotoTestSecretC+`","enable":true},`+
			`{"email":"dave","secret":"`+mtprotoTestSecretD+`","enable":true}]}`)
	mtInbound := loadInboundByTag(t, "mt-windows")
	seedInboundConflict(t, "sibling-vless", "", 46102, model.VLESS, "", `{"clients":[]}`)
	sibling := loadInboundByTag(t, "sibling-vless")
	// The shared stats row may point to another inbound. MTProto must still
	// exclude this disabled client by email.
	if err := database.GetDB().Create(&xray.ClientTraffic{InboundId: sibling.Id, Email: "alice", Enable: false}).Error; err != nil {
		t.Fatal(err)
	}
	clients := []model.ClientRecord{
		{Email: "bob", SubID: "bob-window", Enable: true, WindowQuotaBytes: 100, WindowHours: 2, WindowMode: "rolling"},
		{Email: "carol", SubID: "carol-window", Enable: true},
		{Email: "dave", SubID: "dave-window", Enable: true},
	}
	if err := database.GetDB().Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	links := []model.ClientInbound{
		{ClientId: clients[1].Id, InboundId: mtInbound.Id, WindowQuotaBytes: 100, WindowHours: 2, WindowMode: "fixed"},
		{ClientId: clients[2].Id, InboundId: mtInbound.Id, WindowQuotaBytes: 100, WindowHours: 2, WindowMode: "fixed", WindowExhaustAction: "throttle"},
	}
	if err := database.GetDB().Create(&links).Error; err != nil {
		t.Fatal(err)
	}
	bucket := time.Now().UnixMilli() / windowBucketMillis * windowBucketMillis
	samples := []model.ClientWindowSample{
		{ClientId: clients[0].Id, InboundId: 0, BucketStart: bucket, Bytes: 100},
		{ClientId: clients[1].Id, InboundId: mtInbound.Id, BucketStart: bucket, Bytes: 100},
		{ClientId: clients[2].Id, InboundId: mtInbound.Id, BucketStart: bucket, Bytes: 100},
	}
	if err := database.GetDB().Create(&samples).Error; err != nil {
		t.Fatal(err)
	}
	instances, err := svc.DesiredMtprotoInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 0 {
		t.Fatalf("all disabled/exhausted secrets must be withheld: %+v", instances)
	}
	built, err := svc.buildInboundForLocalRuntime(database.GetDB(), mtInbound)
	if err != nil {
		t.Fatal(err)
	}
	if inst, ok := mtproto.InstanceFromInbound(built); ok {
		t.Fatalf("an interactive client edit must not re-add stopped secrets: %+v", inst)
	}
	if err := database.GetDB().Delete(&model.ClientWindowSample{}, "client_id IN ?", []int{clients[0].Id, clients[1].Id, clients[2].Id}).Error; err != nil {
		t.Fatal(err)
	}
	instances, err = svc.DesiredMtprotoInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || len(instances[0].Secrets) != 3 {
		t.Fatalf("window reset must restore three active secrets, while alice stays disabled: %+v", instances)
	}
	built, err = svc.buildInboundForLocalRuntime(database.GetDB(), mtInbound)
	if err != nil {
		t.Fatal(err)
	}
	pushInst, ok := mtproto.InstanceFromInbound(built)
	if !ok || len(pushInst.Secrets) != 3 {
		t.Fatalf("interactive push must restore reset-window secrets: %+v, ok=%v", pushInst, ok)
	}
}
