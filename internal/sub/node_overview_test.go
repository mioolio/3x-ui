package sub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func int64Ptr(v int64) *int64 { return &v }

func TestNodeOverviewUsesLowestDirectionalMaximumAndPublicWindow(t *testing.T) {
	initSubDB(t)
	db := database.GetDB()
	client := model.ClientRecord{
		Email: "overview@example.com", SubID: "overview", Enable: true,
		SpeedLimitKbps: 10_000, SpeedLimitUpKbps: int64Ptr(8_000), SpeedLimitDownKbps: int64Ptr(12_000),
	}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{
		Remark: "Berlin", Tag: "in-overview", Protocol: model.Protocol("vless"), Enable: true,
		SpeedLimitKbps: 15_000, TrafficMultiplierBps: 20_000,
	}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	link := model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id, SpeedLimitUpKbps: int64Ptr(6_000), SpeedLimitDownKbps: int64Ptr(0)}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	window := service.WindowStatus{QuotaBytes: 10 << 30, UsedBytes: 4 << 30, RemainingBytes: 6 << 30, WindowHours: 2, WindowMode: "fixed", ResetAt: 1_700_000_000_000}
	nodes := (&SubService{}).loadNodeOverview("overview", []NodeWindowQuota{{ClientId: client.Id, InboundId: inbound.Id, WindowStatus: window}}, nil)
	if len(nodes) != 1 {
		t.Fatalf("nodes=%d, want 1", len(nodes))
	}
	got := nodes[0]
	if got.MaxUpKbps != 6_000 || got.MaxDownKbps != 12_000 {
		t.Fatalf("max up/down=%d/%d, want 6000/12000", got.MaxUpKbps, got.MaxDownKbps)
	}
	if got.TrafficMultiplierBps != 20_000 || got.Window == nil || got.Window.RemainingBytes != 6<<30 {
		t.Fatalf("node terms=%+v, want 2x and 6 GiB remaining", got)
	}
	if !got.UsageTracked || got.WindowConfigured {
		t.Fatalf("node tracking flags=%+v, want tracked without configured link window", got)
	}
	if err := db.Model(&inbound).Update("traffic_multiplier_bps", 100).Error; err != nil {
		t.Fatal(err)
	}
	nodes = (&SubService{}).loadNodeOverview("overview", nil, nil)
	if len(nodes) != 1 || nodes[0].TrafficMultiplierBps != 100 {
		t.Fatalf("discount node terms=%+v, want 0.01x", nodes)
	}
}

func TestActiveNodeMultiplierOnlyExposesBillableOverage(t *testing.T) {
	account := &SubPolicyStatus{EffectiveState: "throttled", WindowExhausted: true, WindowAction: "throttle", WindowMultiplierBps: 3_700_000}
	link := &NodeWindowQuota{Action: "throttle", MultiplierBps: 4_000_000, WindowStatus: service.WindowStatus{QuotaBytes: 1000, RemainingBytes: 0}}
	for _, tc := range []struct {
		name       string
		base       int
		account    *SubPolicyStatus
		link       *NodeWindowQuota
		configured bool
		want       int
	}{
		{"global window exhausted", 500_000, account, nil, false, 3_700_000},
		{"link window takes maximum", 500_000, account, link, true, 4_000_000},
		{"larger base remains effective", 5_000_000, account, link, true, 0},
		{"link status unavailable", 500_000, account, nil, true, 0},
		{"link stopped", 500_000, account, &NodeWindowQuota{Action: "stop", WindowStatus: service.WindowStatus{QuotaBytes: 1000}}, true, 0},
		{"account blocked", 500_000, &SubPolicyStatus{EffectiveState: "blocked", WindowExhausted: true, WindowAction: "throttle", WindowMultiplierBps: 3_700_000}, nil, false, 0},
		{"no exhaustion", 500_000, &SubPolicyStatus{EffectiveState: "active", WindowAction: "throttle", WindowMultiplierBps: 3_700_000}, nil, false, 0},
		{"link under allowance", 500_000, &SubPolicyStatus{EffectiveState: "active"}, &NodeWindowQuota{Action: "throttle", MultiplierBps: 4_000_000, WindowStatus: service.WindowStatus{QuotaBytes: 1000, RemainingBytes: 1}}, true, 0},
		{"total exhaustion", 500_000, &SubPolicyStatus{EffectiveState: "throttled", TotalExhausted: true, TotalAction: "throttle", TotalMultiplierBps: 3_700_000}, nil, false, 3_700_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := activeNodeMultiplierBps(tc.base, tc.account, tc.link, tc.configured); got != tc.want {
				t.Fatalf("active factor=%d, want %d", got, tc.want)
			}
		})
	}
}

func TestNodeOverviewPublishesActiveWindowMultiplierOnly(t *testing.T) {
	initSubDB(t)
	db := database.GetDB()
	client := model.ClientRecord{Email: "active-factor@example.com", SubID: "active-factor", Enable: true,
		WindowQuotaBytes: 1000, WindowHours: 2, WindowMode: "fixed", WindowExhaustAction: "throttle", WindowOverageMultiplierBps: 3_700_000}
	inbound := model.Inbound{Tag: "active-factor", Protocol: model.VLESS, Enable: true, TrafficMultiplierBps: 500_000}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	window := service.WindowStatus{QuotaBytes: 1000, RemainingBytes: 0, UsedBytes: 1000}
	nodes := (&SubService{}).loadNodeOverview(client.SubID, nil, []AccountWindowQuota{{AccountIndex: 1, Window: window}})
	if len(nodes) != 1 || nodes[0].TrafficMultiplierBps != 500_000 || nodes[0].ActiveMultiplierBps != 3_700_000 {
		t.Fatalf("active global window factor=%+v", nodes)
	}
	window.RemainingBytes = 1
	nodes = (&SubService{}).loadNodeOverview(client.SubID, nil, []AccountWindowQuota{{AccountIndex: 1, Window: window}})
	if len(nodes) != 1 || nodes[0].ActiveMultiplierBps != 0 {
		t.Fatalf("window not exhausted=%+v", nodes)
	}
	if err := db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", client.Id, inbound.Id).
		Updates(map[string]any{"window_quota_bytes": 1000, "window_hours": 2, "window_mode": "fixed"}).Error; err != nil {
		t.Fatal(err)
	}
	linkWindow := NodeWindowQuota{ClientId: client.Id, InboundId: inbound.Id, Action: "throttle", MultiplierBps: 4_000_000,
		WindowStatus: service.WindowStatus{QuotaBytes: 1000, RemainingBytes: 0, UsedBytes: 1000}}
	nodes = (&SubService{}).loadNodeOverview(client.SubID, []NodeWindowQuota{linkWindow}, []AccountWindowQuota{{AccountIndex: 1, Window: window}})
	if len(nodes) != 1 || nodes[0].ActiveMultiplierBps != 4_000_000 {
		t.Fatalf("active link factor=%+v", nodes)
	}
	linkWindow.Action = "stop"
	nodes = (&SubService{}).loadNodeOverview(client.SubID, []NodeWindowQuota{linkWindow}, []AccountWindowQuota{{AccountIndex: 1, Window: window}})
	if len(nodes) != 1 || nodes[0].ActiveMultiplierBps != 0 {
		t.Fatalf("stopped link must not advertise overage=%+v", nodes)
	}
}

func TestSharedSubscriptionKeepsEachClientNodeAndWindowSeparate(t *testing.T) {
	initSubDB(t)
	db := database.GetDB()
	clients := []model.ClientRecord{
		{Email: "a@example.com", SubID: "shared", Enable: true, SpeedLimitKbps: 10_000, WindowQuotaBytes: 1000, WindowHours: 2, WindowMode: "fixed"},
		{Email: "b@example.com", SubID: "shared", Enable: true, SpeedLimitKbps: 20_000, WindowQuotaBytes: 2000, WindowHours: 2, WindowMode: "fixed"},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{Remark: "Shared node", Tag: "in-shared", Protocol: model.VLESS, Enable: true}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	links := []model.ClientInbound{
		{ClientId: clients[0].Id, InboundId: inbound.Id, WindowQuotaBytes: 500, WindowHours: 2, WindowMode: "fixed"},
		{ClientId: clients[1].Id, InboundId: inbound.Id, WindowQuotaBytes: 900, WindowHours: 2, WindowMode: "fixed"},
	}
	if err := db.Create(&links).Error; err != nil {
		t.Fatal(err)
	}
	bucket := time.Now().UnixMilli() / 60_000 * 60_000
	samples := []model.ClientWindowSample{
		{ClientId: clients[0].Id, InboundId: 0, BucketStart: bucket, Bytes: 100},
		{ClientId: clients[1].Id, InboundId: 0, BucketStart: bucket, Bytes: 300},
		{ClientId: clients[0].Id, InboundId: inbound.Id, BucketStart: bucket, Bytes: 50},
		{ClientId: clients[1].Id, InboundId: inbound.Id, BucketStart: bucket, Bytes: 200},
	}
	if err := db.Create(&samples).Error; err != nil {
		t.Fatal(err)
	}
	first, accountWindows, nodeWindows := (&SubService{}).loadWindowQuotaPage("shared")
	if first == nil || first.RemainingBytes != 900 || len(accountWindows) != 2 {
		t.Fatalf("account windows first=%+v all=%+v", first, accountWindows)
	}
	if accountWindows[0].AccountIndex != 1 || accountWindows[0].Window.RemainingBytes != 900 ||
		accountWindows[1].AccountIndex != 2 || accountWindows[1].Window.RemainingBytes != 1700 {
		t.Fatalf("global windows collapsed across accounts: %+v", accountWindows)
	}
	if len(nodeWindows) != 2 || nodeWindows[0].ClientId != clients[0].Id || nodeWindows[0].RemainingBytes != 450 ||
		nodeWindows[1].ClientId != clients[1].Id || nodeWindows[1].RemainingBytes != 700 {
		t.Fatalf("node windows collapsed across accounts: %+v", nodeWindows)
	}
	nodes := (&SubService{}).loadNodeOverview("shared", nodeWindows, accountWindows)
	if len(nodes) != 2 || nodes[0].AccountIndex != 1 || nodes[0].MaxUpKbps != 10_000 || nodes[0].Window == nil || nodes[0].Window.RemainingBytes != 450 ||
		nodes[1].AccountIndex != 2 || nodes[1].MaxUpKbps != 20_000 || nodes[1].Window == nil || nodes[1].Window.RemainingBytes != 700 {
		t.Fatalf("node details collapsed across accounts: %+v", nodes)
	}
	if !nodes[0].WindowConfigured || !nodes[1].WindowConfigured {
		t.Fatalf("configured per-node windows must be marked: %+v", nodes)
	}
	withoutRemoteStatus := (&SubService{}).loadNodeOverview("shared", nil, accountWindows)
	if len(withoutRemoteStatus) != 2 || !withoutRemoteStatus[0].WindowConfigured || withoutRemoteStatus[0].Window != nil {
		t.Fatalf("missing remote status must not look like shared allowance: %+v", withoutRemoteStatus)
	}
}

func TestTUICNodeOverviewDoesNotAdvertiseUnsupportedMultiplier(t *testing.T) {
	initSubDB(t)
	db := database.GetDB()
	client := model.ClientRecord{Email: "tuic@example.com", SubID: "tuic-public", Enable: true, WindowQuotaBytes: 1000, WindowHours: 2, SpeedLimitKbps: 1000}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{Tag: "tuic-public", Protocol: model.TUIC, Enable: true, TrafficMultiplierBps: 20_000, SpeedLimitKbps: 5000}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id, WindowQuotaBytes: 500, WindowHours: 2, SpeedLimitKbps: 2000}).Error; err != nil {
		t.Fatal(err)
	}
	nodes := (&SubService{}).loadNodeOverview(client.SubID, nil, nil)
	if len(nodes) != 1 || nodes[0].TrafficMultiplierBps != model.DefaultTrafficMultiplierBps || nodes[0].UsageTracked || nodes[0].WindowConfigured || nodes[0].Window != nil {
		t.Fatalf("TUIC must not promise unimplemented usage/multiplier: %+v", nodes)
	}
	if nodes[0].MaxUpKbps != 5000 || nodes[0].MaxDownKbps != 5000 {
		t.Fatalf("TUIC maximum must be the enforced inbound cap, not per-client values: %+v", nodes[0])
	}
	global, account, perNode := (&SubService{}).loadWindowQuotaPage(client.SubID)
	if global != nil || len(account) != 0 || len(perNode) != 0 {
		t.Fatalf("TUIC-only subscription must not advertise unenforced windows: global=%+v account=%+v node=%+v", global, account, perNode)
	}
}

func TestSharedSubscriptionPublicStateEvaluatesEachAccount(t *testing.T) {
	initSubDB(t)
	db := database.GetDB()
	clients := []model.ClientRecord{
		{Email: "active@example.com", SubID: "shared-state", Enable: true, TotalGB: 1_000, ExpiryTime: time.Now().Add(2 * time.Hour).UnixMilli()},
		{Email: "spent@example.com", SubID: "shared-state", Enable: true, TotalGB: 5_000, TotalExhaustAction: "stop", ExpiryTime: time.Now().Add(3 * time.Hour).UnixMilli()},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]xray.ClientTraffic{
		{Email: clients[0].Email, Up: 100},
		{Email: clients[1].Email, Up: 5_100},
	}).Error; err != nil {
		t.Fatal(err)
	}
	page := (&SubService{}).BuildPageData("shared-state", "", xray.ClientTraffic{Up: 5_200, Total: 6_000, Enable: true}, 0,
		[]string{"vless://active", "vless://spent"}, []string{clients[0].Email, clients[1].Email}, "", "", "", "/", "", "")
	if page.PublicState != "mixed" || len(page.AccountStates) != 2 {
		t.Fatalf("shared status=%q, accounts=%+v; want mixed and two separate accounts", page.PublicState, page.AccountStates)
	}
	if page.AccountStates[0].State != "active" || page.AccountStates[1].State != "blocked" {
		t.Fatalf("account states=%+v; first account must not inherit the other's spent quota", page.AccountStates)
	}
	if page.AccountStates[0].AccountIndex != 1 || page.AccountStates[1].AccountIndex != 2 {
		t.Fatalf("public account labels=%+v", page.AccountStates)
	}
	if page.AccountStates[0].ExpiryMs != clients[0].ExpiryTime || page.AccountStates[1].ExpiryMs != clients[1].ExpiryTime {
		t.Fatalf("account deadlines=%+v", page.AccountStates)
	}
}

func TestSharedSubscriptionDoesNotReplaceLinksWithSingleAccountInfoNode(t *testing.T) {
	s := &SubService{subInfoNodeEnable: true, subscriptionBody: true}
	mode, remark := s.resolveInfoNodeRemark("shared", []string{"active@example.com", "spent@example.com"}, xray.ClientTraffic{Up: 1000, Total: 500}, true)
	if mode != infoNodeNone || remark != "" {
		t.Fatalf("shared info node=%v %q; no single account may hide all real links", mode, remark)
	}
}

func TestSharedSubscriptionHeaderDoesNotUseFirstAccountsGraceDeadline(t *testing.T) {
	initSubDB(t)
	clients := []model.ClientRecord{
		{Email: "grace@example.com", SubID: "shared-header", Enable: true, ExpiryTime: time.Now().Add(-time.Hour).UnixMilli(), GraceHours: 4, GraceUpKbps: 100, GraceDownKbps: 100},
		{Email: "paid@example.com", SubID: "shared-header", Enable: true, ExpiryTime: time.Now().Add(24 * time.Hour).UnixMilli()},
	}
	if err := database.GetDB().Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	aggregate := xray.ClientTraffic{ExpiryTime: 0}
	got := (&SubService{}).subscriptionHeaderTraffic("shared-header", aggregate)
	if got.ExpiryTime != 0 {
		t.Fatalf("shared expiry=%d; the first account's grace must not overwrite the aggregate", got.ExpiryTime)
	}
}

func TestDisplayDirectionalRateLayerZeroAndInherit(t *testing.T) {
	if got := displayDirectionalRate(10_000, nil); got != 10_000 {
		t.Fatalf("inherited=%d, want 10000", got)
	}
	if got := displayDirectionalRate(10_000, int64Ptr(0)); got != 0 {
		t.Fatalf("explicit unlimited=%d, want 0", got)
	}
	if got := displayMinPositive(0, 15_000, 10_000); got != 10_000 {
		t.Fatalf("effective maximum=%d, want 10000", got)
	}
}

func TestSubPageContextOmitsPrivateTrafficRules(t *testing.T) {
	initSubDB(t)
	context := (&SUBController{}).subPageContext(PageData{
		SId: "public", PublicState: "active",
		Emails:        []string{"a@example.com"},
		AccountStates: []AccountPublicStatus{{AccountIndex: 1, State: "active"}},
		WindowQuotas:  []AccountWindowQuota{{AccountIndex: 1, Window: service.WindowStatus{QuotaBytes: 1000}}},
		Nodes:         []NodeOverview{{InboundId: 1, AccountIndex: 1, TrafficMultiplierBps: 20_000}},
	})
	for _, key := range []string{"policyStatus", "nodeWindows", "totalAction", "windowAction", "graceHours", "chargedUsedBytes"} {
		if _, present := context[key]; present {
			t.Fatalf("public context unexpectedly contains %q", key)
		}
	}
	if context["publicState"] != "active" {
		t.Fatalf("public state=%v, want active", context["publicState"])
	}
	encoded, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"totalExhaustAction", "windowExhaustAction", "graceQuotaBytes", "overageMultiplierBps", "throttled", "clientEmail", "a@example.com"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("public context contains private strategy %q: %s", private, encoded)
		}
	}
}
