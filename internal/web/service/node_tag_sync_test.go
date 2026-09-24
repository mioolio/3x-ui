package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// A node-backed inbound whose central tag carries the n<id>- prefix must
// survive a snapshot in which the node reports the bare tag (prefix lives on
// the central side only). Before the fix the orphan sweep matched snapTags
// exactly, so it deleted and recreated the inbound on every sync — churning
// its id and dropping traffic for that cycle.
func TestSetRemoteTraffic_KeepsInboundOnPrefixMismatch(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()

	const nodeID = 1
	id := nodeID
	central := &model.Inbound{
		UserId:   1,
		NodeID:   &id,
		Tag:      "n1-in-443-tcp",
		Enable:   true,
		Port:     443,
		Protocol: model.VLESS,
		Settings: `{"clients":[]}`,
	}
	if err := db.Create(central).Error; err != nil {
		t.Fatalf("create node inbound: %v", err)
	}
	centralID := central.Id

	snap := &runtime.TrafficSnapshot{
		Inbounds: []*model.Inbound{{
			Tag:      "in-443-tcp",
			Enable:   true,
			Port:     443,
			Protocol: model.VLESS,
			Settings: `{"clients":[]}`,
			Up:       1000,
			Down:     2000,
		}},
	}

	svc := InboundService{}
	if _, err := svc.setRemoteTrafficLocked(nodeID, snap, false, false); err != nil {
		t.Fatalf("setRemoteTrafficLocked: %v", err)
	}

	var rows []model.Inbound
	if err := db.Where("node_id = ?", nodeID).Find(&rows).Error; err != nil {
		t.Fatalf("list node inbounds: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 node inbound (no churn), got %d", len(rows))
	}
	if rows[0].Id != centralID {
		t.Fatalf("inbound was deleted+recreated: id %d -> %d", centralID, rows[0].Id)
	}
	if rows[0].Up != 1000 || rows[0].Down != 2000 {
		t.Fatalf("traffic not attributed across prefix mismatch: up=%d down=%d", rows[0].Up, rows[0].Down)
	}
}

func TestSetRemoteTraffic_AdoptsCompatibleOriginAliasWithoutDuplicate(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()

	const nodeID = 1
	if err := db.Create(&model.Node{Id: nodeID, Name: "node", Address: "10.0.0.2", Port: 2053, ApiToken: "t", Guid: "node-guid"}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	id := nodeID
	central := &model.Inbound{UserId: 1, NodeID: &id, OriginNodeGuid: "node-guid", Tag: "desired-name", Enable: true, Port: 8443, Protocol: model.VLESS, Settings: `{"clients":[]}`}
	if err := db.Create(central).Error; err != nil {
		t.Fatalf("create central inbound: %v", err)
	}

	snap := &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{{
		Tag: "already-deployed", Enable: true, Port: 8443, Protocol: model.VLESS,
		Settings: `{"clients":[]}`, Up: 11, Down: 22,
	}}}
	if _, err := (&InboundService{}).setRemoteTrafficLocked(nodeID, snap, false, false); err != nil {
		t.Fatalf("setRemoteTrafficLocked: %v", err)
	}

	var rows []model.Inbound
	if err := db.Where("node_id = ?", nodeID).Find(&rows).Error; err != nil {
		t.Fatalf("list node inbounds: %v", err)
	}
	if len(rows) != 1 || rows[0].Id != central.Id || rows[0].Tag != "desired-name" {
		t.Fatalf("alias adoption rows = %#v, want original central inbound only", rows)
	}
	if rows[0].Up != 11 || rows[0].Down != 22 {
		t.Fatalf("alias traffic = %d/%d, want 11/22", rows[0].Up, rows[0].Down)
	}
}

// A node can genuinely own both a bare tag and a tag that happens to look
// like this node's central-only prefix. The exact tags must win over synthetic
// aliases, or both snapshots update the same central row while the other row
// stays visible with stale data.
func TestSetRemoteTraffic_ExactTagWinsOverPrefixAlias(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()
	const nodeID = 1
	id := nodeID
	bare := &model.Inbound{UserId: 1, NodeID: &id, Tag: "free", Remark: "bare", Enable: true, Port: 443, Protocol: model.VLESS, Settings: `{"clients":[]}`}
	prefixed := &model.Inbound{UserId: 1, NodeID: &id, Tag: "n1-free", Remark: "prefixed", Enable: true, Port: 8443, Protocol: model.VLESS, Settings: `{"clients":[]}`}
	for _, inbound := range []*model.Inbound{bare, prefixed} {
		if err := db.Create(inbound).Error; err != nil {
			t.Fatalf("create node inbound %q: %v", inbound.Tag, err)
		}
	}
	snap := &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{
		{Tag: "free", Remark: "bare", Enable: true, Port: 443, Protocol: model.VLESS, Settings: `{"clients":[]}`, Up: 11, Down: 22},
		{Tag: "n1-free", Remark: "prefixed", Enable: true, Port: 8443, Protocol: model.VLESS, Settings: `{"clients":[]}`, Up: 33, Down: 44},
	}}
	if _, err := (&InboundService{}).setRemoteTrafficLocked(nodeID, snap, false, false); err != nil {
		t.Fatalf("setRemoteTrafficLocked: %v", err)
	}
	var rows []model.Inbound
	if err := db.Where("node_id = ?", nodeID).Find(&rows).Error; err != nil {
		t.Fatalf("list node inbounds: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("node inbound rows = %d, want 2 distinct physical inbounds", len(rows))
	}
	byID := map[int]model.Inbound{}
	for _, row := range rows {
		byID[row.Id] = row
	}
	if got := byID[bare.Id]; got.Port != 443 || got.Up != 11 || got.Down != 22 || got.Remark != "bare" {
		t.Fatalf("bare inbound mutated by alias collision: %+v", got)
	}
	if got := byID[prefixed.Id]; got.Port != 8443 || got.Up != 33 || got.Down != 44 || got.Remark != "prefixed" {
		t.Fatalf("prefixed inbound mutated by alias collision: %+v", got)
	}
}

// A tag collision with a local inbound forces the central copy to use n<id>-
// while the node can keep reporting the bare tag. Repeated syncs, including
// a node-side prefix flip, must keep one central ID for that physical inbound.
func TestSetRemoteTraffic_OneRemoteInboundStaysOneAcrossPrefixFlips(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()
	const nodeID = 1
	if err := db.Create(&model.Node{Id: nodeID, Name: "B", Address: "b.test", Port: 2053, Guid: "b-guid"}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := db.Create(&model.Inbound{UserId: 1, Tag: "free", Enable: true, Port: 443, Protocol: model.VLESS, Settings: `{"clients":[]}`}).Error; err != nil {
		t.Fatalf("create local tag collision: %v", err)
	}
	var centralID int
	for index, tag := range []string{"free", "free", "n1-free", "free"} {
		snap := &runtime.TrafficSnapshot{Inbounds: []*model.Inbound{{
			Tag: tag, Enable: true, Port: 8443, Protocol: model.VLESS, Settings: `{"clients":[]}`,
		}}}
		if _, err := (&InboundService{}).setRemoteTrafficLocked(nodeID, snap, false, false); err != nil {
			t.Fatalf("sync %d with tag %q: %v", index, tag, err)
		}
		var rows []model.Inbound
		if err := db.Where("node_id = ?", nodeID).Find(&rows).Error; err != nil {
			t.Fatalf("list remote inbounds after sync %d: %v", index, err)
		}
		if len(rows) != 1 || rows[0].Tag != "n1-free" {
			t.Fatalf("sync %d produced central rows %#v, want one prefixed B inbound", index, rows)
		}
		if index == 0 {
			centralID = rows[0].Id
		} else if rows[0].Id != centralID {
			t.Fatalf("sync %d changed inbound ID from %d to %d", index, centralID, rows[0].Id)
		}
	}
}
