package dispatcher

// Panel bandwidth policy. This file is kept in the bundled Xray fork because
// upstream Xray has no per-user or per-inbound bandwidth shaper. The panel
// atomically replaces the JSON policy file; existing connections see changes
// on their next write without reconnecting.

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/transport/pipe"
	"golang.org/x/time/rate"
)

type panelRatePolicy struct {
	Inbounds           map[string]int64                `json:"inbounds"`
	Clients            map[string]int64                `json:"clients"`
	Overrides          map[string]int64                `json:"overrides"`
	InboundDirections  map[string]panelDirectionalRate `json:"inboundDirections"`
	InboundMultipliers map[string]int64                `json:"inboundMultipliers"`
	ClientDirections   map[string]panelDirectionalRate `json:"clientDirections"`
	OverrideDirections map[string]panelDirectionalRate `json:"overrideDirections"`
	Blocked            map[string]bool                 `json:"blocked"`
	Quotas             map[string]panelQuota           `json:"quotas"`
	TotalQuotas        map[string]panelQuota           `json:"totalQuotas"`
	WindowQuotas       map[string]panelQuota           `json:"windowQuotas"`
	Grace              map[string]panelGrace           `json:"grace"`
}

type panelDirectionalRate struct {
	UpKbps   int64 `json:"upKbps"`
	DownKbps int64 `json:"downKbps"`
}

func panelSelectedRate(legacy map[string]int64, directional map[string]panelDirectionalRate, key, direction string) int64 {
	if value, found := directional[key]; found {
		if direction == "up" {
			return value.UpKbps
		}
		return value.DownKbps
	}
	return legacy[key]
}

type panelQuota struct {
	Remaining     int64  `json:"remaining"`
	Epoch         int64  `json:"epoch"`
	Action        string `json:"action"`
	UpKbps        int64  `json:"upKbps"`
	DownKbps      int64  `json:"downKbps"`
	MultiplierBps int64  `json:"multiplierBps"`
}

type panelGrace struct {
	StartsAt  int64 `json:"startsAt"`
	EndsAt    int64 `json:"endsAt"`
	Remaining int64 `json:"remaining"`
	Epoch     int64 `json:"epoch"`
	UpKbps    int64 `json:"upKbps"`
	DownKbps  int64 `json:"downKbps"`
}

type panelQuotaState struct {
	configured int64
	epoch      int64
	left       int64
	fraction   int64 // Uncharged 1/10000-byte units for the total allowance.
}

var panelRates = struct {
	sync.Mutex
	path           string
	checked        time.Time
	modified       time.Time
	policy         panelRatePolicy
	limiters       map[string]*rate.Limiter
	quotas         map[string]panelQuotaState
	chargeFraction map[string]int64
}{
	limiters:       make(map[string]*rate.Limiter),
	quotas:         make(map[string]panelQuotaState),
	chargeFraction: make(map[string]int64),
}

type panelClientGuard struct {
	sync.Mutex
	references int
}

var panelClientGuards = struct {
	sync.Mutex
	clients map[string]*panelClientGuard
}{clients: make(map[string]*panelClientGuard)}

// Keep one client's reservation, throttle wait, and final write in order.
// This makes cancellation refunds and fractional billing exact even when the
// same email has concurrent sessions on different inbounds. Idle guards are
// removed so long-lived panels do not retain deleted client emails.
func panelAcquireClient(email string) func() {
	panelClientGuards.Lock()
	guard := panelClientGuards.clients[email]
	if guard == nil {
		guard = new(panelClientGuard)
		panelClientGuards.clients[email] = guard
	}
	guard.references++
	panelClientGuards.Unlock()
	guard.Lock()
	return func() {
		guard.Unlock()
		panelClientGuards.Lock()
		guard.references--
		if guard.references == 0 {
			delete(panelClientGuards.clients, email)
		}
		panelClientGuards.Unlock()
	}
}

type panelQuotaRef struct {
	key   string
	rule  panelQuota
	state panelQuotaState
}

type panelThrottle struct {
	key  string
	rate int64
}

type panelDecision struct {
	allowed        bool
	extra          int64
	discount       int64
	graceReserved  bool
	throttles      []panelThrottle
	fractionBefore int64
	fractionAfter  int64
}

type panelWaitResult struct {
	policy   panelRatePolicy
	decision panelDecision
}

func panelQuotaRate(rule panelQuota, direction string) int64 {
	if direction == "up" {
		return rule.UpKbps
	}
	return rule.DownKbps
}

func panelGraceRate(rule panelGrace, direction string) int64 {
	if direction == "up" {
		return rule.UpKbps
	}
	return rule.DownKbps
}

func panelMultiplier(value int64) int64 {
	if value < 10_000 {
		return 10_000
	}
	// A malformed policy cannot overflow byte accounting for a buffer.
	return min(value, 1_000_000_000)
}

func panelInboundMultiplier(policy panelRatePolicy, pair string) int64 {
	tag, _, _ := strings.Cut(pair, "\x00")
	factor := policy.InboundMultipliers[tag]
	if factor < 100 || factor > 9_007_199_254_740_991 {
		return 10_000
	}
	return factor
}

func panelSubtract(value, amount int64) int64 {
	if amount > 0 && value < math.MinInt64+amount {
		return math.MinInt64
	}
	return value - amount
}

// The panel's persisted balance is authoritative when it falls. A fixed-window
// epoch change or rolling-window increase may restore allowance. Total and
// grace allowance increases require a new epoch so late polls cannot refill
// locally reserved bytes.
func panelState(key string, remaining, epoch int64, allowIncrease bool) panelQuotaState {
	state, found := panelRates.quotas[key]
	if !found || state.epoch != epoch || (allowIncrease && remaining > state.configured) {
		state.left = remaining
	} else if remaining < state.left {
		state.left = remaining
	}
	state.configured = remaining
	state.epoch = epoch
	panelRates.quotas[key] = state
	return state
}

// panelBillableBytes splits one buffer at every window and total allowance
// boundary. Each physical byte is billed at the largest active multiplier,
// never at a product of overlapping multipliers. Fractional billable bytes are
// carried across buffers for the same client.
func panelBillableBytes(bytes, totalLeft, fraction int64, total *panelQuotaRef, windows []panelQuotaRef) (int64, int64) {
	return panelBillableBytesAtFactor(bytes, totalLeft, fraction, 10_000, total, windows)
}

func panelBillableBytesAtFactor(bytes, totalLeft, fraction, inboundFactor int64, total *panelQuotaRef, windows []panelQuotaRef) (int64, int64) {
	var position, billed int64
	for position < bytes {
		step := bytes - position
		factor := inboundFactor
		for _, window := range windows {
			if window.rule.Action != "throttle" {
				continue
			}
			if window.state.left <= position {
				factor = max(factor, panelMultiplier(window.rule.MultiplierBps))
			} else if boundary := window.state.left - position; boundary < step {
				step = boundary
			}
		}
		if total != nil && total.rule.Action == "throttle" {
			remaining := panelSubtract(totalLeft, billed)
			if remaining <= 0 {
				factor = max(factor, panelMultiplier(total.rule.MultiplierBps))
			} else if remaining <= (math.MaxInt64-fraction)/10_000 {
				// The next physical byte that reaches the total threshold
				// switches subsequent bytes to the overage multiplier.
				numerator := remaining*10_000 - fraction
				threshold := numerator / factor
				if numerator%factor != 0 {
					threshold++
				}
				if threshold > 0 && threshold < step {
					step = threshold
				}
			}
		}
		if step <= 0 {
			step = 1
		}
		if step > (math.MaxInt64-fraction)/factor {
			return math.MaxInt64, 0
		}
		numerator := fraction + step*factor
		increment := numerator / 10_000
		fraction = numerator % 10_000
		if billed > math.MaxInt64-increment {
			return math.MaxInt64, 0
		}
		billed += increment
		position += step
	}
	return billed, fraction
}

// Reserve quotas under one lock before a buffer is forwarded. Missing new
// window rules fall back to the previous hard-stop `quotas` JSON contract.
func panelDecide(policy panelRatePolicy, email, pair, direction string, bytes int64, now time.Time) panelDecision {
	panelRates.Lock()
	defer panelRates.Unlock()
	if bytes <= 0 {
		return panelDecision{allowed: true}
	}

	var total *panelQuotaRef
	if rule, ok := policy.TotalQuotas[email]; ok {
		ref := panelQuotaRef{key: "total:" + email, rule: rule}
		ref.state = panelState(ref.key, rule.Remaining, rule.Epoch, false)
		total = &ref
	} else {
		delete(panelRates.quotas, "total:"+email)
	}

	windows := make([]panelQuotaRef, 0, 2)
	for _, key := range [2]string{email, pair} {
		rule, found := policy.WindowQuotas[key]
		if !found {
			rule, found = policy.Quotas[key]
		}
		stateKey := "window:" + key
		if !found {
			delete(panelRates.quotas, stateKey)
			continue
		}
		ref := panelQuotaRef{key: stateKey, rule: rule}
		ref.state = panelState(stateKey, rule.Remaining, rule.Epoch, true)
		windows = append(windows, ref)
	}

	var grace *panelQuotaState
	graceRule, hasGrace := policy.Grace[email]
	graceKey := "grace:" + email
	if !hasGrace {
		delete(panelRates.quotas, graceKey)
	} else if now.UnixMilli() >= graceRule.StartsAt {
		if graceRule.EndsAt <= now.UnixMilli() {
			return panelDecision{}
		}
		// The panel sends a physical-byte balance. A negative value means
		// another connection has already exceeded the grace allowance.
		if graceRule.Remaining <= 0 {
			return panelDecision{}
		}
		state := panelState(graceKey, graceRule.Remaining, graceRule.Epoch, false)
		grace = &state
		if state.left < bytes {
			return panelDecision{}
		}
	}

	decision := panelDecision{allowed: true}
	for _, window := range windows {
		if window.state.left >= bytes {
			continue
		}
		if window.rule.Action != "throttle" {
			return panelDecision{}
		}
		if speed := panelQuotaRate(window.rule, direction); speed > 0 {
			decision.throttles = append(decision.throttles, panelThrottle{window.key, speed})
		}
	}

	fraction := panelRates.chargeFraction[email]
	decision.fractionBefore = fraction
	totalLeft := int64(0)
	if total != nil {
		totalLeft = total.state.left
	}
	billed, nextFraction := panelBillableBytesAtFactor(bytes, totalLeft, fraction, panelInboundMultiplier(policy, pair), total, windows)
	if total != nil {
		if total.state.left < billed {
			if total.rule.Action != "throttle" {
				return panelDecision{}
			}
			if speed := panelQuotaRate(total.rule, direction); speed > 0 {
				decision.throttles = append(decision.throttles, panelThrottle{total.key, speed})
			}
		}
		total.state.left = panelSubtract(total.state.left, billed)
		panelRates.quotas[total.key] = total.state
	}
	for _, window := range windows {
		window.state.left = panelSubtract(window.state.left, bytes)
		panelRates.quotas[window.key] = window.state
	}
	if grace != nil {
		grace.left = panelSubtract(grace.left, bytes)
		panelRates.quotas[graceKey] = *grace
		decision.graceReserved = true
	}
	if hasGrace && now.UnixMilli() >= graceRule.StartsAt {
		if speed := panelGraceRate(graceRule, direction); speed > 0 {
			decision.throttles = append(decision.throttles, panelThrottle{graceKey, speed})
		}
	}
	panelRates.chargeFraction[email] = nextFraction
	decision.fractionAfter = nextFraction
	decision.extra = max(0, billed-bytes)
	decision.discount = max(0, bytes-billed)
	return decision
}

// Preview the slow limits without reserving bytes. Most throttled buffers can
// wait outside the per-client commit lock, so upload and download keep their
// independent bandwidth ceilings. The final decision is repeated under that
// lock because another connection or a policy refresh may change the budget.
func panelPreviewThrottles(policy panelRatePolicy, email, pair, direction string, bytes int64, now time.Time) []panelThrottle {
	panelRates.Lock()
	defer panelRates.Unlock()
	var total *panelQuotaRef
	if rule, found := policy.TotalQuotas[email]; found {
		ref := panelQuotaRef{key: "total:" + email, rule: rule}
		ref.state = panelPeekState(ref.key, rule.Remaining, rule.Epoch, false)
		total = &ref
	}
	windows := make([]panelQuotaRef, 0, 2)
	throttles := make([]panelThrottle, 0, 4)
	for _, key := range [2]string{email, pair} {
		rule, found := policy.WindowQuotas[key]
		if !found {
			rule, found = policy.Quotas[key]
		}
		if !found {
			continue
		}
		ref := panelQuotaRef{key: "window:" + key, rule: rule}
		ref.state = panelPeekState(ref.key, rule.Remaining, rule.Epoch, true)
		windows = append(windows, ref)
		if rule.Action == "throttle" && ref.state.left < bytes {
			if speed := panelQuotaRate(rule, direction); speed > 0 {
				throttles = append(throttles, panelThrottle{ref.key, speed})
			}
		}
	}
	if total != nil && total.rule.Action == "throttle" {
		billed, _ := panelBillableBytesAtFactor(bytes, total.state.left, panelRates.chargeFraction[email], panelInboundMultiplier(policy, pair), total, windows)
		if total.state.left < billed {
			if speed := panelQuotaRate(total.rule, direction); speed > 0 {
				throttles = append(throttles, panelThrottle{total.key, speed})
			}
		}
	}
	if grace, found := policy.Grace[email]; found && now.UnixMilli() >= grace.StartsAt && now.UnixMilli() < grace.EndsAt {
		if speed := panelGraceRate(grace, direction); speed > 0 {
			throttles = append(throttles, panelThrottle{"grace:" + email, speed})
		}
	}
	return throttles
}

func panelPeekState(key string, remaining, epoch int64, allowIncrease bool) panelQuotaState {
	state, found := panelRates.quotas[key]
	if !found || state.epoch != epoch || (allowIncrease && remaining > state.configured) {
		state.left = remaining
	} else if remaining < state.left {
		state.left = remaining
	}
	return state
}

// Kept for legacy quota tests and callers with a boolean-only decision.
func panelReserve(policy panelRatePolicy, email, pair string, bytes int64) bool {
	return panelDecide(policy, email, pair, "up", bytes, time.Now()).allowed
}

func panelRefundState(key string, amount, remaining, epoch int64) {
	state, found := panelRates.quotas[key]
	if !found || state.epoch != epoch || state.configured != remaining {
		return
	}
	if state.left > math.MaxInt64-amount {
		state.left = state.configured
	} else {
		state.left = min(state.configured, state.left+amount)
	}
	panelRates.quotas[key] = state
}

// A canceled throttle wait did not forward its reserved buffer. Refund only
// unchanged quota epochs/configurations; a concurrent policy refresh may have
// lowered the authoritative balance and must never be undone here.
func panelRefund(policy panelRatePolicy, email, pair string, bytes int64, decision panelDecision) {
	panelRates.Lock()
	defer panelRates.Unlock()
	if total, found := policy.TotalQuotas[email]; found {
		panelRefundState("total:"+email, bytes+decision.extra-decision.discount, total.Remaining, total.Epoch)
	}
	for _, key := range [2]string{email, pair} {
		window, found := policy.WindowQuotas[key]
		if !found {
			window, found = policy.Quotas[key]
		}
		if found {
			panelRefundState("window:"+key, bytes, window.Remaining, window.Epoch)
		}
	}
	if decision.graceReserved {
		if grace, found := policy.Grace[email]; found {
			panelRefundState("grace:"+email, bytes, grace.Remaining, grace.Epoch)
		}
	}
	if panelRates.chargeFraction[email] == decision.fractionAfter {
		panelRates.chargeFraction[email] = decision.fractionBefore
	}
}

func panelPolicy() panelRatePolicy {
	path := os.Getenv("XRAY_PANEL_RATE_FILE")
	if path == "" {
		return panelRatePolicy{}
	}
	panelRates.Lock()
	defer panelRates.Unlock()
	now := time.Now()
	if panelRates.path == path && now.Before(panelRates.checked.Add(time.Second)) {
		return panelRates.policy
	}
	panelRates.checked = now
	info, err := os.Stat(path)
	if err != nil {
		// A deleted policy must never leave a stale restriction in place.
		panelRates.policy = panelRatePolicy{}
		panelRates.path = path
		panelRates.modified = time.Time{}
		return panelRates.policy
	}
	if panelRates.path == path && info.ModTime().Equal(panelRates.modified) {
		return panelRates.policy
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return panelRates.policy
	}
	var next panelRatePolicy
	if err := json.Unmarshal(data, &next); err != nil {
		return panelRates.policy
	}
	panelRates.policy = next
	panelRates.path = path
	panelRates.modified = info.ModTime()
	return next
}

func panelLimiter(key string, kbps int64) *rate.Limiter {
	panelRates.Lock()
	defer panelRates.Unlock()
	bytesPerSecond := rate.Limit(float64(kbps) * 1000 / 8)
	burst := int(bytesPerSecond)
	if burst < 1024 {
		burst = 1024
	}
	if burst > 64*1024 {
		burst = 64 * 1024
	}
	limiter := panelRates.limiters[key]
	if limiter == nil {
		limiter = rate.NewLimiter(bytesPerSecond, burst)
		panelRates.limiters[key] = limiter
	} else {
		limiter.SetLimit(bytesPerSecond)
		limiter.SetBurst(burst)
	}
	return limiter
}

type panelRateWriter struct {
	buf.Writer
	ctx                   context.Context
	tag, email, direction string
	chargeCounter         stats.Counter
	discountCounter       stats.Counter
}

type panelRateReader struct {
	buf.TimeoutReader
	throttle panelRateWriter
	counter  stats.Counter
}

func (r *panelRateReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.TimeoutReader.ReadMultiBuffer()
	if err != nil || mb.IsEmpty() {
		return mb, err
	}
	prewaited, err := r.throttle.preWait(mb)
	if err != nil {
		buf.ReleaseMulti(mb)
		return nil, err
	}
	release := panelAcquireClient(r.throttle.email)
	defer release()
	result, err := r.throttle.commitWait(mb, prewaited)
	if err != nil {
		buf.ReleaseMulti(mb)
		return nil, err
	}
	if r.counter != nil {
		r.counter.Add(int64(mb.Len()))
	}
	if result.decision.extra > 0 && r.throttle.chargeCounter != nil {
		r.throttle.chargeCounter.Add(result.decision.extra)
	}
	if result.decision.discount > 0 && r.throttle.discountCounter != nil {
		r.throttle.discountCounter.Add(result.decision.discount)
	}
	return mb, nil
}

func (r *panelRateReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	mb, err := r.TimeoutReader.ReadMultiBufferTimeout(timeout)
	if err != nil || mb.IsEmpty() {
		return mb, err
	}
	prewaited, err := r.throttle.preWait(mb)
	if err != nil {
		buf.ReleaseMulti(mb)
		return nil, err
	}
	release := panelAcquireClient(r.throttle.email)
	defer release()
	result, err := r.throttle.commitWait(mb, prewaited)
	if err != nil {
		buf.ReleaseMulti(mb)
		return nil, err
	}
	if r.counter != nil {
		r.counter.Add(int64(mb.Len()))
	}
	if result.decision.extra > 0 && r.throttle.chargeCounter != nil {
		r.throttle.chargeCounter.Add(result.decision.extra)
	}
	if result.decision.discount > 0 && r.throttle.discountCounter != nil {
		r.throttle.discountCounter.Add(result.decision.discount)
	}
	return mb, nil
}

func (w *panelRateWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	prewaited, err := w.preWait(mb)
	if err != nil {
		buf.ReleaseMulti(mb)
		return err
	}
	release := panelAcquireClient(w.email)
	defer release()
	bytes := int64(mb.Len())
	result, err := w.commitWait(mb, prewaited)
	if err != nil {
		buf.ReleaseMulti(mb)
		return err
	}
	if err := w.Writer.WriteMultiBuffer(mb); err != nil {
		// The regular dispatcher writes into an all-or-nothing pipe. An
		// arbitrary external writer can partially forward before returning an
		// error, so its reservation stays spent rather than risking bypass.
		if panelAtomicWriter(w.Writer) {
			panelRefund(result.policy, w.email, w.tag+"\x00"+w.email, bytes, result.decision)
		}
		return err
	}
	if result.decision.extra > 0 && w.chargeCounter != nil {
		w.chargeCounter.Add(result.decision.extra)
	}
	if result.decision.discount > 0 && w.discountCounter != nil {
		w.discountCounter.Add(result.decision.discount)
	}
	return nil
}

func panelAtomicWriter(writer buf.Writer) bool {
	switch typed := writer.(type) {
	case *pipe.Writer:
		return true
	case *SizeStatWriter:
		return panelAtomicWriter(typed.Writer)
	default:
		return false
	}
}

func (w *panelRateWriter) waitLimit(key string, kbps int64, bytes int) error {
	if kbps <= 0 {
		return nil
	}
	limiter := panelLimiter(key+":"+w.direction, kbps)
	for bytes > 0 {
		chunk := min(bytes, limiter.Burst())
		if err := limiter.WaitN(w.ctx, chunk); err != nil {
			return err
		}
		bytes -= chunk
	}
	return nil
}

func (w *panelRateWriter) preWait(mb buf.MultiBuffer) (map[string]int64, error) {
	policy := panelPolicy()
	key := w.tag + "\x00" + w.email
	if policy.Blocked[w.email] || policy.Blocked[key] {
		return nil, context.Canceled
	}
	limits := [3]struct {
		key  string
		rate int64
	}{
		{"inbound:" + w.tag, panelSelectedRate(policy.Inbounds, policy.InboundDirections, w.tag, w.direction)},
		{"client:" + w.email, panelSelectedRate(policy.Clients, policy.ClientDirections, w.email, w.direction)},
		{"override:" + key, panelSelectedRate(policy.Overrides, policy.OverrideDirections, key, w.direction)},
	}
	for _, entry := range limits {
		if err := w.waitLimit(entry.key, entry.rate, int(mb.Len())); err != nil {
			return nil, err
		}
	}
	policy = panelPolicy()
	if policy.Blocked[w.email] || policy.Blocked[key] {
		return nil, context.Canceled
	}
	prewaited := make(map[string]int64)
	for _, entry := range panelPreviewThrottles(policy, w.email, key, w.direction, int64(mb.Len()), time.Now()) {
		if err := w.waitLimit(entry.key, entry.rate, int(mb.Len())); err != nil {
			return nil, err
		}
		prewaited[entry.key] = entry.rate
	}
	return prewaited, nil
}

func (w *panelRateWriter) commitWait(mb buf.MultiBuffer, prewaited map[string]int64) (panelWaitResult, error) {
	// The preview ran without a reservation. Re-read policy and state under
	// the client's commit lock after all ordinary/known-slow waits finish.
	policy := panelPolicy()
	key := w.tag + "\x00" + w.email
	if policy.Blocked[w.email] || policy.Blocked[key] {
		return panelWaitResult{}, context.Canceled
	}
	decision := panelDecide(policy, w.email, key, w.direction, int64(mb.Len()), time.Now())
	if !decision.allowed {
		return panelWaitResult{}, context.Canceled
	}
	for _, entry := range decision.throttles {
		if prior := prewaited[entry.key]; prior > 0 && prior <= entry.rate {
			continue
		}
		if err := w.waitLimit(entry.key, entry.rate, int(mb.Len())); err != nil {
			panelRefund(policy, w.email, key, int64(mb.Len()), decision)
			return panelWaitResult{}, err
		}
	}
	latest := panelPolicy()
	if latest.Blocked[w.email] || latest.Blocked[key] {
		panelRefund(policy, w.email, key, int64(mb.Len()), decision)
		return panelWaitResult{}, context.Canceled
	}
	if grace, found := latest.Grace[w.email]; found && time.Now().UnixMilli() >= grace.EndsAt {
		panelRefund(policy, w.email, key, int64(mb.Len()), decision)
		return panelWaitResult{}, context.Canceled
	}
	return panelWaitResult{policy: policy, decision: decision}, nil
}

func panelChargeCounter(manager stats.Manager, tag, email, direction string) stats.Counter {
	if manager == nil {
		return nil
	}
	counter, _ := manager.GetOrRegisterCounter(panelChargeName(tag, email, direction))
	return counter
}

func panelDiscountCounter(manager stats.Manager, tag, email, direction string) stats.Counter {
	if manager == nil {
		return nil
	}
	counter, _ := manager.GetOrRegisterCounter(panelDiscountName(tag, email, direction))
	return counter
}

func panelWrapWriters(ctx context.Context, manager stats.Manager, tag, email string, up, down buf.Writer) (buf.Writer, buf.Writer) {
	if tag == "" || email == "" || os.Getenv("XRAY_PANEL_RATE_FILE") == "" {
		return up, down
	}
	return &panelRateWriter{Writer: up, ctx: ctx, tag: tag, email: email, direction: "up", chargeCounter: panelChargeCounter(manager, tag, email, "uplink"), discountCounter: panelDiscountCounter(manager, tag, email, "uplink")},
		&panelRateWriter{Writer: down, ctx: ctx, tag: tag, email: email, direction: "down", chargeCounter: panelChargeCounter(manager, tag, email, "downlink"), discountCounter: panelDiscountCounter(manager, tag, email, "downlink")}
}

func panelWrapReader(ctx context.Context, manager stats.Manager, tag, email string, reader buf.TimeoutReader, counter stats.Counter) buf.TimeoutReader {
	if tag == "" || email == "" {
		return reader
	}
	return &panelRateReader{TimeoutReader: reader, throttle: panelRateWriter{ctx: ctx, tag: tag, email: email, direction: "up", chargeCounter: panelChargeCounter(manager, tag, email, "uplink"), discountCounter: panelDiscountCounter(manager, tag, email, "uplink")}, counter: counter}
}

func panelTrafficName(tag, email, direction string) string {
	return "panel>>>" + url.QueryEscape(tag) + ">>>" + url.QueryEscape(email) + ">>>traffic>>>" + direction
}

func panelChargeName(tag, email, direction string) string {
	return "panel>>>" + url.QueryEscape(tag) + ">>>" + url.QueryEscape(email) + ">>>chargeExtra>>>" + direction
}

func panelDiscountName(tag, email, direction string) string {
	return "panel>>>" + url.QueryEscape(tag) + ">>>" + url.QueryEscape(email) + ">>>chargeDiscount>>>" + direction
}
