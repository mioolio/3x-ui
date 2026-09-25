package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func multiplierPtr[T any](value T) *T { return &value }

func TestClientOverageMultiplierSupportsCustomAndExactJSONRange(t *testing.T) {
	for _, factor := range []int{10_000, 3_700_000, 1_000_000_000, model.MaxOverageMultiplierBps} {
		factor := factor
		input := model.Client{
			TotalOverageMultiplierBps:  &factor,
			WindowOverageMultiplierBps: &factor,
		}
		if err := validateClientPolicy(input, nil); err != nil {
			t.Fatalf("factor %d unexpectedly rejected: %v", factor, err)
		}
	}
	for _, factor := range []int{9_999, model.MaxOverageMultiplierBps + 1} {
		factor := factor
		input := model.Client{WindowOverageMultiplierBps: &factor}
		if err := validateClientPolicy(input, nil); err == nil || !strings.Contains(err.Error(), "windowOverageMultiplierBps") {
			t.Fatalf("factor %d should be rejected without truncation, got %v", factor, err)
		}
	}
}

func TestLinkedInboundOverageMultiplierRoundTrip(t *testing.T) {
	setupBulkDB(t)
	db := database.GetDB()
	inbound := model.Inbound{Tag: "large-overage-inbound", Protocol: model.VLESS, Enable: true}
	client := model.ClientRecord{Email: "large-overage@example.invalid", Enable: true}
	if err := db.Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	factor := 3_700_000
	if err := new(ClientService).SetInboundWindowQuotas(client.Email, map[int]InboundWindowQuota{
		inbound.Id: {QuotaBytes: 1024, Hours: 2, WindowExhaustAction: multiplierPtr("throttle"), WindowExhaustUpKbps: multiplierPtr(int64(64)), WindowExhaustDownKbps: multiplierPtr(int64(64)), WindowOverageMultiplierBps: &factor},
	}); err != nil {
		t.Fatal(err)
	}
	var link model.ClientInbound
	if err := db.Where("client_id = ? AND inbound_id = ?", client.Id, inbound.Id).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.WindowOverageMultiplierBps != factor {
		t.Fatalf("saved factor %d, want %d", link.WindowOverageMultiplierBps, factor)
	}
	policies, err := loadInboundLinkPolicies(db, inbound.Id)
	if err != nil || len(policies) != 1 {
		t.Fatalf("load linked policy: count=%d err=%v", len(policies), err)
	}
	policies[0].WindowOverageMultiplierBps = model.MaxOverageMultiplierBps
	if err := new(ClientService).SetInboundLinkPolicies(inbound.Id, policies); err != nil {
		t.Fatalf("maximum factor rejected by linked policy sync: %v", err)
	}
	if err := db.Where("client_id = ? AND inbound_id = ?", client.Id, inbound.Id).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.WindowOverageMultiplierBps != model.MaxOverageMultiplierBps {
		t.Fatalf("linked policy factor %d, want exact max", link.WindowOverageMultiplierBps)
	}
	policies[0].WindowOverageMultiplierBps++
	if err := new(ClientService).SetInboundLinkPolicies(inbound.Id, policies); err == nil {
		t.Fatal("linked policy accepted a multiplier that JSON cannot round-trip exactly")
	}
}
