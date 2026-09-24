package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/transport/pipe"
)

type panelTestCounter struct{ value int64 }

func (c *panelTestCounter) Value() int64 { return c.value }
func (c *panelTestCounter) Set(value int64) int64 {
	previous := c.value
	c.value = value
	return previous
}
func (c *panelTestCounter) Add(value int64) int64 {
	previous := c.value
	c.value += value
	return previous
}

type panelTestStatsManager struct {
	stats.NoopManager
	counters map[string]*panelTestCounter
}

func (m *panelTestStatsManager) GetOrRegisterCounter(name string) (stats.Counter, error) {
	if m.counters == nil {
		m.counters = make(map[string]*panelTestCounter)
	}
	if m.counters[name] == nil {
		m.counters[name] = new(panelTestCounter)
	}
	return m.counters[name], nil
}

type panelTestWriter struct{ fail bool }

func (w *panelTestWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	buf.ReleaseMulti(mb)
	if w.fail {
		return errors.New("test writer failed")
	}
	return nil
}

func panelTestBuffer(bytes int32) buf.MultiBuffer {
	b := buf.New()
	b.Extend(bytes)
	return buf.MultiBuffer{b}
}

func TestPanelDirectionalRatesPreserveLegacyAndZeroOverride(t *testing.T) {
	var policy panelRatePolicy
	if err := json.Unmarshal([]byte(`{
		"inbounds":{"inbound":1000},
		"clients":{"client":2000},
		"overrides":{"inbound\u0000client":3000},
		"inboundDirections":{"inbound":{"upKbps":120,"downKbps":0}},
		"overrideDirections":{"inbound\u0000client":{"upKbps":0,"downKbps":350}}
	}`), &policy); err != nil {
		t.Fatal(err)
	}
	if got := panelSelectedRate(policy.Inbounds, policy.InboundDirections, "inbound", "up"); got != 120 {
		t.Fatalf("inbound upload = %d, want 120", got)
	}
	if got := panelSelectedRate(policy.Inbounds, policy.InboundDirections, "inbound", "down"); got != 0 {
		t.Fatalf("inbound download = %d, want unlimited (0)", got)
	}
	if got := panelSelectedRate(policy.Clients, policy.ClientDirections, "client", "down"); got != 2000 {
		t.Fatalf("legacy client download = %d, want 2000", got)
	}
	pair := "inbound\x00client"
	if got := panelSelectedRate(policy.Overrides, policy.OverrideDirections, pair, "up"); got != 0 {
		t.Fatalf("pair upload = %d, want unlimited (0)", got)
	}
	if got := panelSelectedRate(policy.Overrides, policy.OverrideDirections, pair, "down"); got != 350 {
		t.Fatalf("pair download = %d, want 350", got)
	}
	if got := panelSelectedRate(policy.Overrides, policy.OverrideDirections, "missing", "down"); got != 0 {
		t.Fatalf("missing pair download = %d, want unlimited (0)", got)
	}
}

func TestPanelWrapWritersDirection(t *testing.T) {
	t.Setenv("XRAY_PANEL_RATE_FILE", "test-policy.json")
	up, down := panelWrapWriters(context.Background(), nil, "inbound", "client", nil, nil)
	if got := up.(*panelRateWriter).direction; got != "up" {
		t.Fatalf("uplink writer direction = %q", got)
	}
	if got := down.(*panelRateWriter).direction; got != "down" {
		t.Fatalf("downlink writer direction = %q", got)
	}
}

func TestPanelReserve(t *testing.T) {
	const email, pair = "panel-test@example.invalid", "panel-test-inbound\x00panel-test@example.invalid"
	policy := panelRatePolicy{Quotas: map[string]panelQuota{
		email: {Remaining: 100, Epoch: 1},
		pair:  {Remaining: 60, Epoch: 1},
	}}
	if !panelReserve(policy, email, pair, 40) {
		t.Fatal("first buffer should pass")
	}
	if panelReserve(policy, email, pair, 30) {
		t.Fatal("per-inbound quota must stop the second buffer")
	}
	if !panelReserve(policy, email, pair, 20) {
		t.Fatal("exact remaining amount should pass")
	}
	if panelReserve(policy, email, pair, 1) {
		t.Fatal("quota must be exhausted")
	}
	// A late panel report can only lower the budget; it cannot restore bytes
	// that the core already reserved after the stats snapshot was taken.
	policy.Quotas[pair] = panelQuota{Remaining: 40, Epoch: 1}
	if panelReserve(policy, email, pair, 1) {
		t.Fatal("late poll must not restore consumed bytes")
	}
	policy.Quotas[pair] = panelQuota{Remaining: 60, Epoch: 2}
	policy.Quotas[email] = panelQuota{Remaining: 100, Epoch: 2}
	if !panelReserve(policy, email, pair, 60) {
		t.Fatal("new fixed window must restore quota")
	}
}

func TestPanelQuotaHardStopIsImmediate(t *testing.T) {
	const email = "hard-stop@example.invalid"
	policy := panelRatePolicy{TotalQuotas: map[string]panelQuota{
		email: {Remaining: 10, Epoch: 1},
	}}
	now := time.UnixMilli(1_800_000_000_000)
	if decision := panelDecide(policy, email, "inbound\x00"+email, "down", 6, now); !decision.allowed {
		t.Fatal("first buffer should pass")
	}
	if decision := panelDecide(policy, email, "inbound\x00"+email, "down", 5, now); decision.allowed {
		t.Fatal("buffer crossing total allowance must be stopped")
	}
	if decision := panelDecide(policy, email, "inbound\x00"+email, "down", 4, now); !decision.allowed {
		t.Fatal("rejected buffer must not consume allowance")
	}
}

func TestPanelQuotaResetAndHardStopPriority(t *testing.T) {
	const email = "reset-priority@example.invalid"
	pair := "inbound\x00" + email
	policy := panelRatePolicy{
		TotalQuotas:  map[string]panelQuota{email: {Remaining: 10, Epoch: 1, Action: "throttle", UpKbps: 50}},
		WindowQuotas: map[string]panelQuota{pair: {Remaining: 5, Epoch: 1}},
	}
	now := time.UnixMilli(1_800_000_000_000)
	if decision := panelDecide(policy, email, pair, "up", 5, now); !decision.allowed {
		t.Fatal("first buffer should pass")
	}
	if decision := panelDecide(policy, email, pair, "up", 1, now); decision.allowed {
		t.Fatal("hard window stop must override soft total quota")
	}
	policy.WindowQuotas[pair] = panelQuota{Remaining: 5, Epoch: 2}
	if decision := panelDecide(policy, email, pair, "up", 5, now); !decision.allowed {
		t.Fatal("new window epoch should restore its allowance")
	}
	policy.TotalQuotas[email] = panelQuota{Remaining: 8, Epoch: 1, Action: "throttle", UpKbps: 50}
	panelRates.Lock()
	left := panelRates.quotas["total:"+email].left
	panelRates.Unlock()
	if left != 0 {
		t.Fatalf("total remaining after two buffers = %d, want 0", left)
	}
	if decision := panelDecide(policy, email, pair, "up", 1, now); decision.allowed {
		t.Fatal("window should remain empty after the second buffer")
	}
	panelRates.Lock()
	left = panelRates.quotas["total:"+email].left
	panelRates.Unlock()
	if left != 0 {
		t.Fatalf("same-epoch reported increase refilled total to %d", left)
	}
	policy.TotalQuotas[email] = panelQuota{Remaining: 8, Epoch: 2, Action: "throttle", UpKbps: 50}
	policy.WindowQuotas[pair] = panelQuota{Remaining: 5, Epoch: 3}
	if decision := panelDecide(policy, email, pair, "up", 5, now); !decision.allowed || len(decision.throttles) != 0 {
		t.Fatalf("new total and window epochs should restore normal speed: %+v", decision)
	}
}

func TestPanelQuotaOverageUsesMaximumMultiplier(t *testing.T) {
	const email = "overage@example.invalid"
	pair := "inbound\x00" + email
	policy := panelRatePolicy{
		TotalQuotas: map[string]panelQuota{email: {Remaining: 20, Epoch: 1}},
		WindowQuotas: map[string]panelQuota{
			email: {Remaining: 5, Epoch: 1, Action: "throttle", UpKbps: 100, DownKbps: 200, MultiplierBps: 20_000},
			pair:  {Remaining: 8, Epoch: 1, Action: "throttle", UpKbps: 60, DownKbps: 120, MultiplierBps: 30_000},
		},
	}
	decision := panelDecide(policy, email, pair, "down", 10, time.UnixMilli(1_800_000_000_000))
	if !decision.allowed || decision.extra != 8 {
		t.Fatalf("first buffer decision = %+v, want allowed with 8 extra billable bytes", decision)
	}
	if len(decision.throttles) != 2 || decision.throttles[0].rate != 200 || decision.throttles[1].rate != 120 {
		t.Fatalf("window throttles = %+v, want both independent download limits", decision.throttles)
	}
	panelRates.Lock()
	left := panelRates.quotas["total:"+email].left
	panelRates.Unlock()
	if left != 2 {
		t.Fatalf("total allowance left = %d, want 2 (10 physical + 8 extra charged)", left)
	}
	if decision := panelDecide(policy, email, pair, "up", 2, time.UnixMilli(1_800_000_000_000)); decision.allowed {
		t.Fatal("next buffer costs 6 at the larger 3x multiplier and must hit hard total cap")
	}
}

func TestPanelInboundMultiplierChargesTotalAndBothWindows(t *testing.T) {
	const email = "charged-windows@example.invalid"
	pair := "fifty-times\x00" + email
	policy := panelRatePolicy{
		InboundMultipliers: map[string]int64{"fifty-times": 500_000},
		TotalQuotas:        map[string]panelQuota{email: {Remaining: 300, Epoch: 1}},
		WindowQuotas: map[string]panelQuota{
			email: {Remaining: 150, Epoch: 1},
			pair:  {Remaining: 100, Epoch: 1},
		},
	}
	now := time.UnixMilli(1_800_000_000_000)
	first := panelDecide(policy, email, pair, "down", 2, now)
	if !first.allowed || first.charged != 100 || first.extra != 98 {
		t.Fatalf("50x decision = %+v, want 100 charged bytes", first)
	}
	panelRates.Lock()
	total := panelRates.quotas["total:"+email].left
	global := panelRates.quotas["window:"+email].left
	linked := panelRates.quotas["window:"+pair].left
	panelRates.Unlock()
	if total != 200 || global != 50 || linked != 0 {
		t.Fatalf("charged balances = total %d, global %d, linked %d", total, global, linked)
	}
	if rejected := panelDecide(policy, email, pair, "down", 1, now); rejected.allowed {
		t.Fatalf("exhausted linked window allowed another 50x byte: %+v", rejected)
	}
	panelRates.Lock()
	unchanged := panelRates.quotas["total:"+email].left == total && panelRates.quotas["window:"+email].left == global && panelRates.quotas["window:"+pair].left == linked && panelRates.chargeFraction[email] == 0
	panelRates.Unlock()
	if !unchanged {
		t.Fatal("rejected buffer consumed a charged quota or fraction")
	}
}

func TestPanelFractionalInboundMultiplierStopsAtExhaustedWindow(t *testing.T) {
	const email = "fractional-windows@example.invalid"
	pair := "one-hundredth\x00" + email
	policy := panelRatePolicy{
		InboundMultipliers: map[string]int64{"one-hundredth": 100},
		TotalQuotas:        map[string]panelQuota{email: {Remaining: 1, Epoch: 1}},
		WindowQuotas: map[string]panelQuota{
			email: {Remaining: 1, Epoch: 1},
			pair:  {Remaining: 1, Epoch: 1},
		},
	}
	now := time.UnixMilli(1_800_000_000_000)
	for i := 0; i < 99; i++ {
		if decision := panelDecide(policy, email, pair, "up", 1, now); !decision.allowed || decision.charged != 0 {
			t.Fatalf("discounted byte %d = %+v, want zero rounded charge", i+1, decision)
		}
	}
	panelRates.Lock()
	left := panelRates.quotas["window:"+pair].left
	fraction := panelRates.chargeFraction[email]
	panelRates.Unlock()
	if left != 1 || fraction != 9900 {
		t.Fatalf("after 99 physical bytes: left=%d fraction=%d", left, fraction)
	}
	if decision := panelDecide(policy, email, pair, "up", 1, now); !decision.allowed || decision.charged != 1 {
		t.Fatalf("hundredth discounted byte = %+v, want one charged byte", decision)
	}
	if decision := panelDecide(policy, email, pair, "up", 1, now); decision.allowed {
		t.Fatalf("zero remaining hard window allowed an extra discounted byte: %+v", decision)
	}
	panelRates.Lock()
	total := panelRates.quotas["total:"+email].left
	global := panelRates.quotas["window:"+email].left
	linked := panelRates.quotas["window:"+pair].left
	fraction = panelRates.chargeFraction[email]
	panelRates.Unlock()
	if total != 0 || global != 0 || linked != 0 || fraction != 0 {
		t.Fatalf("exhausted balances changed: total=%d global=%d linked=%d fraction=%d", total, global, linked, fraction)
	}
}

func TestPanelWindowOverageStartsAtChargedBoundary(t *testing.T) {
	const email = "charged-overage@example.invalid"
	pair := "double-inbound\x00" + email
	policy := panelRatePolicy{
		InboundMultipliers: map[string]int64{"double-inbound": 20_000},
		TotalQuotas:        map[string]panelQuota{email: {Remaining: 100, Epoch: 1}},
		WindowQuotas: map[string]panelQuota{
			email: {Remaining: 2, Epoch: 1, Action: "throttle", DownKbps: 20, MultiplierBps: 30_000},
			pair:  {Remaining: 5, Epoch: 1, Action: "throttle", DownKbps: 10, MultiplierBps: 40_000},
		},
	}
	now := time.UnixMilli(1_800_000_000_000)
	preview := panelPreviewThrottles(policy, email, pair, "down", 4, now)
	if len(preview) != 2 || preview[0].rate != 20 || preview[1].rate != 10 {
		t.Fatalf("charged-window throttle preview = %+v", preview)
	}
	decision := panelDecide(policy, email, pair, "down", 4, now)
	if !decision.allowed || decision.charged != 13 || decision.extra != 9 || len(decision.throttles) != 2 {
		t.Fatalf("2x inbound, then 3x and 4x overage = %+v, want 13 charged bytes", decision)
	}
	panelRates.Lock()
	total := panelRates.quotas["total:"+email].left
	global := panelRates.quotas["window:"+email].left
	linked := panelRates.quotas["window:"+pair].left
	panelRates.Unlock()
	if total != 87 || global != -11 || linked != -8 {
		t.Fatalf("overage balances = total %d, global %d, linked %d", total, global, linked)
	}
}

func TestPanelPreviewUsesChargedWindowBudgetWithoutReserving(t *testing.T) {
	const email = "charged-preview@example.invalid"
	pair := "preview-fifty\x00" + email
	policy := panelRatePolicy{
		InboundMultipliers: map[string]int64{"preview-fifty": 500_000},
		WindowQuotas: map[string]panelQuota{
			pair: {Remaining: 49, Epoch: 1, Action: "throttle", UpKbps: 25},
		},
	}
	now := time.UnixMilli(1_800_000_000_000)
	preview := panelPreviewThrottles(policy, email, pair, "up", 1, now)
	if len(preview) != 1 || preview[0].key != "window:"+pair || preview[0].rate != 25 {
		t.Fatalf("50x preview for one physical byte = %+v", preview)
	}
	panelRates.Lock()
	_, reserved := panelRates.quotas["window:"+pair]
	panelRates.Unlock()
	if reserved {
		t.Fatal("preview reserved the window allowance")
	}
	decision := panelDecide(policy, email, pair, "up", 1, now)
	if !decision.allowed || decision.charged != 50 || len(decision.throttles) != 1 {
		t.Fatalf("commit differs from charged preview: %+v", decision)
	}
}

func TestPanelRefundRestoresChargedWindowsAndFraction(t *testing.T) {
	const email = "charged-refund@example.invalid"
	pair := "one-and-half\x00" + email
	policy := panelRatePolicy{
		InboundMultipliers: map[string]int64{"one-and-half": 15_000},
		TotalQuotas:        map[string]panelQuota{email: {Remaining: 3, Epoch: 1}},
		WindowQuotas: map[string]panelQuota{
			email: {Remaining: 3, Epoch: 1},
			pair:  {Remaining: 3, Epoch: 1},
		},
	}
	now := time.UnixMilli(1_800_000_000_000)
	first := panelDecide(policy, email, pair, "up", 1, now)
	second := panelDecide(policy, email, pair, "up", 1, now)
	if !first.allowed || first.charged != 1 || !second.allowed || second.charged != 2 {
		t.Fatalf("fractional decisions = %+v then %+v", first, second)
	}
	panelRefund(policy, email, pair, 1, second)
	panelRates.Lock()
	total := panelRates.quotas["total:"+email].left
	global := panelRates.quotas["window:"+email].left
	linked := panelRates.quotas["window:"+pair].left
	fraction := panelRates.chargeFraction[email]
	panelRates.Unlock()
	if total != 2 || global != 2 || linked != 2 || fraction != 5000 {
		t.Fatalf("refunded balances = total %d, global %d, linked %d, fraction %d", total, global, linked, fraction)
	}
	if replay := panelDecide(policy, email, pair, "up", 1, now); !replay.allowed || replay.charged != 2 {
		t.Fatalf("replayed byte after refund = %+v, want two charged bytes", replay)
	}
	if rejected := panelDecide(policy, email, pair, "up", 1, now); rejected.allowed {
		t.Fatalf("buffer crossing hard charged balance = %+v", rejected)
	}
	panelRates.Lock()
	fraction = panelRates.chargeFraction[email]
	panelRates.Unlock()
	if fraction != 0 {
		t.Fatalf("rejected buffer changed fraction to %d", fraction)
	}
}

func TestPanelInboundMultiplierDiscountAndPremium(t *testing.T) {
	const email = "inbound-multiplier@example.invalid"
	now := time.UnixMilli(1_800_000_000_000)
	policy := panelRatePolicy{InboundMultipliers: map[string]int64{"discount": 100, "premium": 20_000, "maximum": 9_007_199_254_740_991}}
	discount := panelDecide(policy, email, "discount\x00"+email, "up", 100, now)
	if !discount.allowed || discount.discount != 99 || discount.extra != 0 {
		t.Fatalf("0.01x accounting = %+v, want one billable byte", discount)
	}
	premium := panelDecide(policy, email, "premium\x00"+email, "down", 10, now)
	if !premium.allowed || premium.extra != 10 || premium.discount != 0 {
		t.Fatalf("2x accounting = %+v, want twenty billable bytes", premium)
	}
	if got := panelInboundMultiplier(policy, "missing\x00"+email); got != 10_000 {
		t.Fatalf("legacy inbound factor = %d, want 1x", got)
	}
	if got := panelInboundMultiplier(policy, "maximum\x00"+email); got != 9_007_199_254_740_991 {
		t.Fatalf("maximum safely representable factor = %d", got)
	}
	if billed, _ := panelBillableBytesAtFactor(1<<63-1, 0, 0, 9_007_199_254_740_991, nil, nil); billed != 1<<63-1 {
		t.Fatalf("large factors must saturate without overflowing: billed %d", billed)
	}
	if name := panelDiscountName("a/b", email, "uplink"); !strings.Contains(name, "a%2Fb") || !strings.HasSuffix(name, ">>>chargeDiscount>>>uplink") {
		t.Fatalf("discount counter name = %q", name)
	}
}

func TestPanelTotalOverageThrottlesAndTracksDebt(t *testing.T) {
	const email = "total-throttle@example.invalid"
	policy := panelRatePolicy{TotalQuotas: map[string]panelQuota{
		email: {Remaining: 5, Epoch: 1, Action: "throttle", UpKbps: 80, DownKbps: 40, MultiplierBps: 20_000},
	}}
	now := time.UnixMilli(1_800_000_000_000)
	decision := panelDecide(policy, email, "inbound\x00"+email, "up", 10, now)
	if !decision.allowed || decision.extra != 5 || len(decision.throttles) != 1 || decision.throttles[0].rate != 80 {
		t.Fatalf("crossing total allowance = %+v", decision)
	}
	decision = panelDecide(policy, email, "inbound\x00"+email, "down", 2, now)
	if !decision.allowed || decision.extra != 2 || len(decision.throttles) != 1 || decision.throttles[0].rate != 40 {
		t.Fatalf("already exhausted total allowance = %+v", decision)
	}
	panelRates.Lock()
	left := panelRates.quotas["total:"+email].left
	panelRates.Unlock()
	if left != -14 {
		t.Fatalf("total balance = %d, want -14", left)
	}
}

func TestPanelGraceStartsAndEndsAtConfiguredTimes(t *testing.T) {
	const email = "grace@example.invalid"
	policy := panelRatePolicy{Grace: map[string]panelGrace{
		email: {StartsAt: 1000, EndsAt: 3000, Remaining: 10, Epoch: 1, UpKbps: 25, DownKbps: 50},
	}}
	pair := "inbound\x00" + email
	if decision := panelDecide(policy, email, pair, "up", 20, time.UnixMilli(999)); !decision.allowed || len(decision.throttles) != 0 {
		t.Fatalf("pre-expiry decision = %+v", decision)
	}
	if decision := panelDecide(policy, email, pair, "up", 8, time.UnixMilli(1000)); !decision.allowed || len(decision.throttles) != 1 || decision.throttles[0].rate != 25 {
		t.Fatalf("grace decision = %+v", decision)
	}
	if decision := panelDecide(policy, email, pair, "down", 3, time.UnixMilli(2000)); decision.allowed {
		t.Fatal("grace traffic allowance must stop the buffer crossing its cap")
	}
	if decision := panelDecide(policy, email, pair, "down", 1, time.UnixMilli(3000)); decision.allowed {
		t.Fatal("grace deadline must stop traffic even with bytes left")
	}
	policy.Grace[email] = panelGrace{StartsAt: 1000, EndsAt: 4000, Remaining: -1, Epoch: 2}
	if decision := panelDecide(policy, email, pair, "down", 1, time.UnixMilli(2000)); decision.allowed {
		t.Fatal("negative grace balance means overused, not unlimited")
	}
}

func TestPanelChargeFractionAndCounterName(t *testing.T) {
	const email = "fraction@example.invalid"
	pair := "a/b\x00" + email
	policy := panelRatePolicy{WindowQuotas: map[string]panelQuota{
		pair: {Remaining: 0, Epoch: 1, Action: "throttle", MultiplierBps: 15_000},
	}}
	now := time.UnixMilli(1_800_000_000_000)
	first := panelDecide(policy, email, pair, "up", 1, now)
	second := panelDecide(policy, email, pair, "up", 1, now)
	if !first.allowed || !second.allowed || first.extra != 0 || second.extra != 1 {
		t.Fatalf("1.5x fractional extra = (%+v, %+v), want 0 then 1", first, second)
	}
	if name := panelChargeName("a/b", email, "uplink"); !strings.Contains(name, "a%2Fb") || !strings.HasSuffix(name, ">>>chargeExtra>>>uplink") {
		t.Fatalf("charge counter name = %q", name)
	}
}

func TestPanelChargeExtraCounterOnlyCountsForwardedBytes(t *testing.T) {
	const tag, email = "charge-counter", "counter@example.invalid"
	path := filepath.Join(t.TempDir(), "rates.json")
	policy := panelRatePolicy{WindowQuotas: map[string]panelQuota{
		tag + "\x00" + email: {Remaining: 0, Epoch: 1, Action: "throttle", MultiplierBps: 20_000},
	}}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_PANEL_RATE_FILE", path)
	manager := new(panelTestStatsManager)
	underlying := new(panelTestWriter)
	up, _ := panelWrapWriters(context.Background(), manager, tag, email, underlying, underlying)
	if err := up.WriteMultiBuffer(panelTestBuffer(10)); err != nil {
		t.Fatal(err)
	}
	name := panelChargeName(tag, email, "uplink")
	if got := manager.counters[name].Value(); got != 10 {
		t.Fatalf("forwarded extra bytes = %d, want 10", got)
	}
	underlying.fail = true
	if err := up.WriteMultiBuffer(panelTestBuffer(5)); err == nil {
		t.Fatal("underlying writer should fail")
	}
	if got := manager.counters[name].Value(); got != 10 {
		t.Fatalf("failed write changed extra counter to %d", got)
	}
}

func TestPanelDiscountCounterOnlyCountsForwardedBytes(t *testing.T) {
	const tag, email = "discount-counter", "discount@example.invalid"
	path := filepath.Join(t.TempDir(), "rates.json")
	data, err := json.Marshal(panelRatePolicy{InboundMultipliers: map[string]int64{tag: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_PANEL_RATE_FILE", path)
	manager := new(panelTestStatsManager)
	underlying := new(panelTestWriter)
	up, _ := panelWrapWriters(context.Background(), manager, tag, email, underlying, underlying)
	if err := up.WriteMultiBuffer(panelTestBuffer(100)); err != nil {
		t.Fatal(err)
	}
	name := panelDiscountName(tag, email, "uplink")
	if got := manager.counters[name].Value(); got != 99 {
		t.Fatalf("forwarded discount bytes = %d, want 99", got)
	}
	underlying.fail = true
	if err := up.WriteMultiBuffer(panelTestBuffer(100)); err == nil {
		t.Fatal("underlying writer should fail")
	}
	if got := manager.counters[name].Value(); got != 99 {
		t.Fatalf("failed write changed discount counter to %d", got)
	}
}

func TestPanelCancelledThrottleRefundsUnsentBuffer(t *testing.T) {
	const tag, email = "cancel-inbound", "cancel@example.invalid"
	path := filepath.Join(t.TempDir(), "rates.json")
	policy := panelRatePolicy{
		TotalQuotas: map[string]panelQuota{email: {Remaining: 5000, Epoch: 1}},
		WindowQuotas: map[string]panelQuota{
			tag + "\x00" + email: {Remaining: 0, Epoch: 1, Action: "throttle", UpKbps: 1, MultiplierBps: 15_000},
		},
	}
	data, _ := json.Marshal(policy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_PANEL_RATE_FILE", path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := new(panelTestStatsManager)
	w := &panelRateWriter{ctx: ctx, tag: tag, email: email, direction: "up", chargeCounter: panelChargeCounter(manager, tag, email, "uplink")}
	mb := panelTestBuffer(2001)
	release := panelAcquireClient(email)
	_, err := w.commitWait(mb, nil) // quota changed after the unreserved preview
	release()
	buf.ReleaseMulti(mb)
	if err == nil {
		t.Fatal("cancelled throttle wait should fail")
	}
	panelRates.Lock()
	totalLeft := panelRates.quotas["total:"+email].left
	windowLeft := panelRates.quotas["window:"+tag+"\x00"+email].left
	fraction := panelRates.chargeFraction[email]
	panelRates.Unlock()
	if totalLeft != 5000 || windowLeft != 0 || fraction != 0 {
		t.Fatalf("cancelled buffer retained reservations: total=%d window=%d fraction=%d", totalLeft, windowLeft, fraction)
	}
	if decision := panelDecide(policy, email, tag+"\x00"+email, "up", 1, time.Now()); !decision.allowed || decision.extra != 0 {
		t.Fatalf("fractional billing after cancellation = %+v, want first byte at 1x", decision)
	}
	if got := manager.counters[panelChargeName(tag, email, "uplink")].Value(); got != 0 {
		t.Fatalf("cancelled buffer charged %d extra bytes", got)
	}
}

func TestPanelFailedAtomicPipeRefundsAndDoesNotCountTraffic(t *testing.T) {
	const tag, email = "pipe-fail-inbound", "pipe-fail@example.invalid"
	path := filepath.Join(t.TempDir(), "rates.json")
	policy := panelRatePolicy{
		TotalQuotas: map[string]panelQuota{email: {Remaining: 100, Epoch: 1}},
		WindowQuotas: map[string]panelQuota{
			tag + "\x00" + email: {Remaining: 0, Epoch: 1, Action: "throttle", MultiplierBps: 15_000},
		},
	}
	data, _ := json.Marshal(policy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_PANEL_RATE_FILE", path)
	_, pipeWriter := pipe.New()
	if err := pipeWriter.Close(); err != nil {
		t.Fatal(err)
	}
	physicalCounter := new(panelTestCounter)
	underlying := &SizeStatWriter{Counter: physicalCounter, Writer: pipeWriter}
	manager := new(panelTestStatsManager)
	up, _ := panelWrapWriters(context.Background(), manager, tag, email, underlying, underlying)
	if err := up.WriteMultiBuffer(panelTestBuffer(11)); err == nil {
		t.Fatal("closed pipe should reject the buffer")
	}
	panelRates.Lock()
	totalLeft := panelRates.quotas["total:"+email].left
	windowLeft := panelRates.quotas["window:"+tag+"\x00"+email].left
	fraction := panelRates.chargeFraction[email]
	panelRates.Unlock()
	if totalLeft != 100 || windowLeft != 0 || fraction != 0 {
		t.Fatalf("failed atomic write retained reservations: total=%d window=%d fraction=%d", totalLeft, windowLeft, fraction)
	}
	if got := physicalCounter.Value(); got != 0 {
		t.Fatalf("failed atomic write counted %d physical bytes", got)
	}
	if got := manager.counters[panelChargeName(tag, email, "uplink")].Value(); got != 0 {
		t.Fatalf("failed atomic write charged %d extra bytes", got)
	}
}

func TestPanelKnownSlowWaitDoesNotHoldClientCommitLock(t *testing.T) {
	const tag, email = "concurrent-inbound", "concurrent@example.invalid"
	path := filepath.Join(t.TempDir(), "rates.json")
	policy := panelRatePolicy{WindowQuotas: map[string]panelQuota{
		tag + "\x00" + email: {Remaining: 0, Epoch: 1, Action: "throttle", UpKbps: 100_000},
	}}
	data, _ := json.Marshal(policy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_PANEL_RATE_FILE", path)
	release := panelAcquireClient(email)
	defer release()
	done := make(chan error, 1)
	go func() {
		mb := panelTestBuffer(1)
		defer buf.ReleaseMulti(mb)
		w := panelRateWriter{ctx: context.Background(), tag: tag, email: email, direction: "up"}
		_, err := w.preWait(mb)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("known slow wait blocked on the client's commit lock")
	}
}

func TestPanelPhysicalCounterStillCountsSuccessfulPipeWrite(t *testing.T) {
	_, writer := pipe.New()
	defer writer.Close()
	counter := new(panelTestCounter)
	wrapped := &SizeStatWriter{Counter: counter, Writer: writer}
	if err := wrapped.WriteMultiBuffer(panelTestBuffer(7)); err != nil {
		t.Fatal(err)
	}
	if got := counter.Value(); got != 7 {
		t.Fatalf("successful write counted %d physical bytes, want 7", got)
	}
}
