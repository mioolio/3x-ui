package job

import (
	"math"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/mtproto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func TestMtprotoBillingCarriesFractionsByInboundAndClient(t *testing.T) {
	j := new(MtprotoJob)
	for i := 0; i < 99; i++ {
		extra, discount := j.billTraffic("discount", "alice", 1, 100)
		if extra != 0 || discount != 1 {
			t.Fatalf("small discounted poll %d = extra %d / discount %d", i, extra, discount)
		}
	}
	extra, discount := j.billTraffic("discount", "alice", 1, 100)
	if extra != 0 || discount != 0 {
		t.Fatalf("hundredth byte = extra %d / discount %d, want one billable byte", extra, discount)
	}
	if extra, discount := j.billTraffic("premium", "alice", 10, 20_000); extra != 10 || discount != 0 {
		t.Fatalf("2x inbound = extra %d / discount %d", extra, discount)
	}
	if extra, discount := j.billTraffic("discount", "bob", 1, 100); extra != 0 || discount != 1 {
		t.Fatalf("separate client fraction = extra %d / discount %d", extra, discount)
	}
	if extra, discount := j.billTraffic("discount", "alice", 10, 10_000); extra != 0 || discount != 0 {
		t.Fatalf("factor changed to 1x = extra %d / discount %d", extra, discount)
	}
}

func TestMtprotoBillingLargeMultipliersSaturateWithoutWrapping(t *testing.T) {
	j := new(MtprotoJob)
	factor := model.MaxTrafficMultiplierBps
	billedOneByte := int64(factor / 10_000)
	if extra, discount := j.billTraffic("max", "alice", 1, factor); extra != billedOneByte-1 || discount != 0 {
		t.Fatalf("one byte at JS-safe maximum = extra %d / discount %d, want extra %d", extra, discount, billedOneByte-1)
	}
	if extra, discount := j.billTraffic("max", "bob", 100_000_000, factor); extra != math.MaxInt64-100_000_000 || discount != 0 {
		t.Fatalf("large sample must saturate billable bytes, got extra %d / discount %d", extra, discount)
	}
	if extra, discount := j.billTraffic("discount", "tiny", math.MaxInt64, 100); extra != 0 || discount < 0 {
		t.Fatalf("discounted max-int sample wrapped: extra %d / discount %d", extra, discount)
	}
}

func TestMtprotoFinalSampleUsesRunningSidecarPolicy(t *testing.T) {
	j := new(MtprotoJob)
	global, windows, inbounds := j.trafficRows([]mtproto.Traffic{{
		Tag: "removed-window", Email: "alice", Up: 7, Down: 3,
		MultiplierBps: 20_000, RouteThroughXray: true, RuntimePolicyKnown: true,
	}}, map[string]int{"removed-window": 10_000}, map[string]bool{"removed-window": false})
	if len(global) != 1 || global[0].ChargeExtraDelta != 10 || global[0].ChargeDiscountDelta != 0 {
		t.Fatalf("final sample used new desired factor instead of running sidecar: %+v", global)
	}
	if global[0].ChargeExtraUpDelta != 7 || global[0].ChargeExtraDownDelta != 3 {
		t.Fatalf("final sample lost directional bill: %+v", global[0])
	}
	if len(windows) != 1 || windows[0].Up != 7 || windows[0].Down != 3 ||
		windows[0].ChargeExtraDelta != 10 || !windows[0].ChargeCountersSeen {
		t.Fatalf("final sample lost per-inbound window bytes: %+v", windows)
	}
	if len(inbounds) != 0 {
		t.Fatalf("Xray-routed final sample would double-count inbound traffic: %+v", inbounds)
	}
}

func TestMtprotoTrafficRowsAggregateSharedEmailButKeepInboundWindows(t *testing.T) {
	setupIntegrationDB(t)
	db := database.GetDB()
	client := &model.ClientRecord{Email: "shared@mtproto.test", SubID: "mtproto-window", Enable: true}
	if err := db.Create(client).Error; err != nil {
		t.Fatal(err)
	}
	first := &model.Inbound{Tag: "mt-discount", Protocol: model.MTProto, Port: 27101, Enable: true, Settings: `{}`}
	second := &model.Inbound{Tag: "mt-premium", Protocol: model.MTProto, Port: 27102, Enable: true, Settings: `{}`}
	if err := db.Create(first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(second).Error; err != nil {
		t.Fatal(err)
	}
	j := new(MtprotoJob)
	global, perInbound, inbounds := j.trafficRows([]mtproto.Traffic{
		{Tag: first.Tag, Email: client.Email, Up: 60, Down: 40},
		{Tag: second.Tag, Email: client.Email, Up: 20, Down: 30},
	}, map[string]int{first.Tag: 100, second.Tag: 20_000}, nil)
	if len(global) != 1 || global[0].Up != 80 || global[0].Down != 70 ||
		global[0].ChargeExtraDelta != 50 || global[0].ChargeDiscountDelta != 99 {
		t.Fatalf("same email must have one correctly billed DB row: %+v", global)
	}
	if global[0].ChargeExtraUpDelta != 20 || global[0].ChargeExtraDownDelta != 30 ||
		global[0].ChargeDiscountUpDelta+global[0].ChargeDiscountDownDelta != 99 {
		t.Fatalf("same email directional bill = %+v", global[0])
	}
	if len(perInbound) != 2 || perInbound[0].Tag != first.Tag || perInbound[1].Tag != second.Tag {
		t.Fatalf("per-inbound window attribution was merged away: %+v", perInbound)
	}
	if len(inbounds) != 2 {
		t.Fatalf("non-routed inbound totals lost: %+v", inbounds)
	}
	if err := (&service.XrayService{}).RecordWindowTraffic(global, perInbound); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, test := range []struct {
		inboundID int
		used      int64
	}{
		{0, 101}, {first.Id, 1}, {second.Id, 100},
	} {
		status, err := service.WindowQuotaStatus(db, client.Id, test.inboundID, 200, 2, "rolling", now)
		if err != nil {
			t.Fatal(err)
		}
		if status.UsedBytes != test.used {
			t.Fatalf("window %d used %d charged bytes, want %d", test.inboundID, status.UsedBytes, test.used)
		}
	}
}
