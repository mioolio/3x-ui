package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestLegacyNodeMissingPolicyRoutesAllowsDefaultsOnly(t *testing.T) {
	setupBulkDB(t)
	useTestRuntimeManager(t)
	f := newFakeNodeHTTP(t)
	const email = "legacy"
	ib := realNodeInbound(t, f, 56601, []model.Client{{Email: email, ID: "55555555-1111-2222-3333-444444444444", SubID: "sub-legacy", Enable: true}})
	f.mu.Lock()
	remoteID := f.tags[ib.Tag]
	f.mu.Unlock()
	f.setMissing("/panel/api/clients/legacy/directionalRates", true)
	f.setMissing("/panel/api/clients/legacy/rates", true)
	f.setMissing("/panel/api/clients/legacy/windowQuotas", true)
	f.setMissing(fmt.Sprintf("/panel/api/clients/inbound/%d/linkPolicies", remoteID), true)

	clients := &ClientService{}
	inbounds := &InboundService{}
	if err := clients.PushInboundDirectionalRates(inbounds, email, map[int]InboundDirectionalRate{ib.Id: {}}); err != nil {
		t.Fatalf("default directional rate on legacy node: %v", err)
	}
	if got := f.hitCount("/clients/legacy/rates"); got != 1 {
		t.Fatalf("legacy zero-rate clear requests = %d, want 1", got)
	}
	if err := clients.PushInboundRates(inbounds, email, map[int]int64{ib.Id: 0}); err != nil {
		t.Fatalf("default symmetric rate on legacy node: %v", err)
	}
	if err := clients.PushInboundWindowQuotas(inbounds, email, map[int]InboundWindowQuota{ib.Id: {}}); err != nil {
		t.Fatalf("default window quota on legacy node: %v", err)
	}
	rt, err := inbounds.runtimeFor(ib)
	if err != nil {
		t.Fatal(err)
	}
	remote, ok := rt.(*runtime.Remote)
	if !ok {
		t.Fatalf("runtime %T, want *runtime.Remote", rt)
	}
	if err := inbounds.syncNodeInboundLinkPolicies(context.Background(), remote, ib); err != nil {
		t.Fatalf("default link policy on legacy node: %v", err)
	}

	if err := clients.PushInboundDirectionalRates(inbounds, email, map[int]InboundDirectionalRate{ib.Id: {UpKbps: 64}}); err == nil || !strings.Contains(err.Error(), "节点需升级") {
		t.Fatalf("nonzero directional rate error = %v, want upgrade required", err)
	}
	if err := clients.PushInboundRates(inbounds, email, map[int]int64{ib.Id: 64}); err == nil || !strings.Contains(err.Error(), "节点需升级") {
		t.Fatalf("nonzero symmetric rate error = %v, want upgrade required", err)
	}
	if err := clients.SetInboundWindowQuotas(email, map[int]InboundWindowQuota{ib.Id: {QuotaBytes: 1024, Hours: 2, Mode: "fixed"}}); err != nil {
		t.Fatal(err)
	}
	if err := clients.PushInboundWindowQuotas(inbounds, email, map[int]InboundWindowQuota{ib.Id: {}}); err == nil || !strings.Contains(err.Error(), "节点需升级") {
		t.Fatalf("nonzero window quota error = %v, want upgrade required", err)
	}
	if err := inbounds.syncNodeInboundLinkPolicies(context.Background(), remote, ib); err == nil || !strings.Contains(err.Error(), "节点需升级") {
		t.Fatalf("nonzero link policy error = %v, want upgrade required", err)
	}
}

func TestModernNodeReceivesDefaultPolicySync(t *testing.T) {
	setupBulkDB(t)
	useTestRuntimeManager(t)
	f := newFakeNodeHTTP(t)
	const email = "modern"
	ib := realNodeInbound(t, f, 56602, []model.Client{{Email: email, ID: "55555555-1111-2222-3333-444444444445", SubID: "sub-modern", Enable: true}})
	clients := &ClientService{}
	inbounds := &InboundService{}
	if err := clients.PushInboundDirectionalRates(inbounds, email, map[int]InboundDirectionalRate{ib.Id: {}}); err != nil {
		t.Fatal(err)
	}
	if err := clients.PushInboundWindowQuotas(inbounds, email, map[int]InboundWindowQuota{ib.Id: {}}); err != nil {
		t.Fatal(err)
	}
	rt, err := inbounds.runtimeFor(ib)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	remoteID := f.tags[ib.Tag]
	f.mu.Unlock()
	if err := inbounds.syncNodeInboundLinkPolicies(context.Background(), rt.(*runtime.Remote), ib); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/clients/modern/directionalRates",
		"/clients/modern/windowQuotas",
		fmt.Sprintf("/clients/inbound/%d/linkPolicies", remoteID),
	} {
		if got := f.hitCount(path); got != 1 {
			t.Fatalf("modern node requests for %s = %d, want 1", path, got)
		}
	}
}
