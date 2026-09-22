package service

import (
	"encoding/json"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/throttle"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// ThrottleEgressInboundTag is the tag of the loopback SOCKS inbound injected so
// the throttle relay can dial destinations back through the core, inheriting
// the default outbound and the admin's routing exactly like other traffic.
const ThrottleEgressInboundTag = "throttle-egress"

// ThrottleOutboundTag is the per-client socks outbound that carries a limited
// client's traffic into the relay; the SOCKS username identifies the client.
func ThrottleOutboundTag(email string) string {
	return "throttle-out-" + email
}

// throttleRow is one clients × client_traffics row with every field the
// effective-limit computation reads, including the strictest inbound-level
// window/plan configuration across the client's inbounds.
type throttleRow struct {
	Email             string
	Enable            bool
	Up                int64
	Down              int64
	Total             int64
	ExpiryTime        int64
	SpeedLimitUp      int
	SpeedLimitDown    int
	DepletionAction   string
	DepletionSpeed    int
	DepletionPeriod   string
	DepletionPeriodGB int64
	// Client-level window config; the effective window the state machine reads.
	WindowQuotaGB int64
	WindowMinutes int
	WindowAction  string
	WindowSpeed   int
	// Inbound-level window config, collapsed to the strictest per email.
	IWindowQuotaGB int64
	IWindowMinutes int
	IWindowAction  string
	IWindowSpeed   int
	WindowUsed     int64
	WindowStarted  int64
	PeriodUsed     int64
	PeriodStarted  int64
	PlanQuotaGB    int64
	PlanMinutes    int
	PlanAction     string
	PlanSpeed      int
}

// ThrottleLimitState is what a client's rate depends on right now. It is the
// testable core of ThrottleLimits.
type ThrottleLimitState struct {
	SpeedUp, SpeedDown int
	Depleted           bool
	DepletionAction    string
	DepletionSpeed     int
	WindowViolated     bool
	WindowAction       string
	WindowSpeed        int
	PeriodViolated     bool
	PeriodAction       string
	PeriodSpeed        int
	WindowUsed         int64
	WindowQuota        int64
	WindowStarted      int64
	WindowMinutes      int
}

// DepletionThrottle reports whether quota/expiry exhaustion should throttle
// rather than disable this client.
func (t ThrottleLimitState) DepletionThrottle() bool {
	return t.Depleted && t.DepletionAction == "throttle"
}

// WindowThrottle reports whether a live window overrun should throttle this
// client until the window slides.
func (t ThrottleLimitState) WindowThrottle() bool {
	return t.WindowViolated && t.WindowAction == "throttle"
}

// WindowExceeded reports a live window overrun regardless of the action, for
// the disable path.
func (t ThrottleLimitState) WindowExceeded() bool {
	return t.WindowViolated
}

// EffectiveLimit resolves the strictest applicable cap; false means no limit
// applies and the client needs no routing rule.
func (t ThrottleLimitState) EffectiveLimit() (throttle.Limit, bool) {
	down, up := t.SpeedDown, t.SpeedUp
	if t.DepletionThrottle() {
		down = strictest(down, t.DepletionSpeed)
		up = strictest(up, t.DepletionSpeed)
	}
	if t.WindowThrottle() {
		down = strictest(down, t.WindowSpeed)
		up = strictest(up, t.WindowSpeed)
	}
	if t.PeriodViolated && t.PeriodAction == "throttle" {
		down = strictest(down, t.PeriodSpeed)
		up = strictest(up, t.PeriodSpeed)
	}
	if down <= 0 && up <= 0 {
		return throttle.Limit{}, false
	}
	return throttle.Limit{DownKbps: down, UpKbps: up}, true
}

// WindowEndTime is when the running window slides, or 0 when none is open.
func (t ThrottleLimitState) WindowEndTime() int64 {
	if t.WindowMinutes <= 0 || t.WindowStarted <= 0 {
		return 0
	}
	return t.WindowStarted + int64(t.WindowMinutes)*60000
}

// strictest keeps the only positive of two caps, or the smaller when both apply.
func strictest(a, b int) int {
	switch {
	case b <= 0:
		return a
	case a <= 0:
		return b
	case b < a:
		return b
	default:
		return a
	}
}

// throttleStateFrom builds the limit state for one row at time now (ms). The
// client's own window config wins over the inbound plan; the plan fence only
// caps speed when its action is throttle (a disable plan is enforced by the
// traffic pipeline, which leaves the row disabled).
func throttleStateFrom(r throttleRow, now int64) ThrottleLimitState {
	depleted := (r.Total > 0 && r.Up+r.Down >= r.Total) ||
		(r.ExpiryTime > 0 && r.ExpiryTime <= now)
	windowEnd := int64(0)
	if r.WindowMinutes > 0 && r.WindowStarted > 0 {
		windowEnd = r.WindowStarted + int64(r.WindowMinutes)*60000
	}
	windowViolated := r.WindowQuotaGB > 0 && r.WindowMinutes > 0 &&
		r.WindowStarted > 0 && now < windowEnd && r.WindowUsed > r.WindowQuotaGB
	periodEnd := int64(0)
	if r.PeriodStarted > 0 && r.PlanMinutes > 0 {
		periodEnd = r.PeriodStarted + int64(r.PlanMinutes)*60000
	}
	periodViolated := r.PlanQuotaGB > 0 && r.PeriodStarted > 0 &&
		now < periodEnd && r.PeriodUsed > r.PlanQuotaGB
	return ThrottleLimitState{
		SpeedUp: r.SpeedLimitUp, SpeedDown: r.SpeedLimitDown,
		Depleted: depleted, DepletionAction: r.DepletionAction, DepletionSpeed: r.DepletionSpeed,
		WindowViolated: windowViolated, WindowAction: r.WindowAction, WindowSpeed: r.WindowSpeed,
		PeriodViolated: periodViolated, PeriodAction: r.PlanAction, PeriodSpeed: r.PlanSpeed,
		WindowUsed: r.WindowUsed, WindowQuota: r.WindowQuotaGB,
		WindowStarted: r.WindowStarted, WindowMinutes: r.WindowMinutes,
	}
}

// ThrottleLimits queries every throttle- or plan-configured client and
// resolves its current effective cap. Only clients on this panel's own
// inbounds are considered — node inbound traffic never routes through the
// local core. The client's own window config wins over the inbound's; across
// several inbounds the strictest plan applies.
func (s *InboundService) ThrottleLimits() map[string]throttle.Limit {
	limits := map[string]throttle.Limit{}
	db := database.GetDB()
	if db == nil {
		return limits
	}
	var rows []throttleRow
	err := db.Table("clients").
		Select(`clients.email AS email,
			client_traffics.enable AS enable,
			client_traffics.up AS up,
			client_traffics.down AS down,
			COALESCE(client_traffics.total, 0) AS total,
			COALESCE(client_traffics.expiry_time, 0) AS expiry_time,
			COALESCE(clients.speed_limit_up, 0) AS speed_limit_up,
			COALESCE(clients.speed_limit_down, 0) AS speed_limit_down,
			COALESCE(clients.depletion_action, '') AS depletion_action,
			COALESCE(clients.depletion_speed, 0) AS depletion_speed,
			COALESCE(clients.depletion_period, '') AS depletion_period,
			COALESCE(clients.depletion_period_gb, 0) AS depletion_period_gb,
			COALESCE(clients.window_quota_gb, 0) AS window_quota_gb,
			COALESCE(clients.window_minutes, 0) AS window_minutes,
			COALESCE(clients.window_action, '') AS window_action,
			COALESCE(clients.window_speed, 0) AS window_speed,
			COALESCE(client_traffics.window_used, 0) AS window_used,
			COALESCE(client_traffics.window_started, 0) AS window_started,
			COALESCE(client_traffics.period_used, 0) AS period_used,
			COALESCE(client_traffics.period_started, 0) AS period_started,
			COALESCE(inbounds.window_quota_gb, 0) AS i_window_quota_gb,
			COALESCE(inbounds.window_minutes, 0) AS i_window_minutes,
			COALESCE(inbounds.window_action, '') AS i_window_action,
			COALESCE(inbounds.window_speed, 0) AS i_window_speed,
			COALESCE(inbounds.plan_quota_gb, 0) AS plan_quota_gb,
			(CASE COALESCE(inbounds.plan_period, '')
				WHEN 'daily' THEN 1440
				WHEN 'weekly' THEN 10080
				WHEN 'monthly' THEN 43200
				ELSE 0 END) AS plan_minutes,
			COALESCE(inbounds.plan_action, '') AS plan_action,
			COALESCE(inbounds.plan_speed, 0) AS plan_speed`).
		Joins("JOIN client_inbounds ON client_inbounds.client_id = clients.id").
		Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id AND inbounds.node_id IS NULL").
		Joins("JOIN client_traffics ON client_traffics.email = clients.email").
		Where(`clients.speed_limit_down > 0 OR clients.speed_limit_up > 0
			OR clients.depletion_action = ?
			OR COALESCE(clients.depletion_period, '') <> ''
			OR COALESCE(clients.window_minutes, 0) > 0
			OR COALESCE(inbounds.window_minutes, 0) > 0
			OR COALESCE(inbounds.plan_period, '') <> ''`, "throttle").
		Find(&rows).Error
	if err != nil {
		logger.Warning("throttle: query limits failed:", err)
		return limits
	}
	now := time.Now().UnixMilli()

	type merged struct {
		row    throttleRow
		window effectivePlan
		plan   effectivePlan
	}
	byEmail := make(map[string]*merged, len(rows))
	for _, r := range rows {
		if r.Email == "" {
			continue
		}
		m := byEmail[r.Email]
		if m == nil {
			m = &merged{row: r}
			byEmail[r.Email] = m
		}
		if m.row.Enable && !r.Enable {
			m.row.Enable = false
		}
		// Strictest inbound-level window across the client's inbounds; the
		// client's own config (kept in m.row on the first row) overrides it.
		if ib := (effectivePlan{quotaGB: r.IWindowQuotaGB, minutes: r.IWindowMinutes, action: r.IWindowAction, speed: r.IWindowSpeed}); ib.minutes > 0 {
			if m.window.minutes == 0 || ib.quotaGB < m.window.quotaGB {
				m.window = ib
			}
		}
		// Strictest plan across inbounds.
		if pl := (effectivePlan{quotaGB: r.PlanQuotaGB, minutes: r.PlanMinutes, action: r.PlanAction, speed: r.PlanSpeed}); pl.minutes > 0 {
			if m.plan.minutes == 0 || pl.quotaGB < m.plan.quotaGB {
				m.plan = pl
			}
		}
	}
	for email, m := range byEmail {
		row := m.row
		if !row.Enable {
			continue
		}
		if row.WindowMinutes == 0 && m.window.minutes > 0 {
			row.WindowQuotaGB, row.WindowMinutes = m.window.quotaGB, m.window.minutes
			row.WindowAction, row.WindowSpeed = m.window.action, m.window.speed
		}
		// While depletion-throttled, the client's own periodic cap replaces
		// the inbound plan for speed purposes (its disable is pipeline-side,
		// driven by period_disabled in the traffic job).
		depleted := (row.Total > 0 && row.Up+row.Down >= row.Total) ||
			(row.ExpiryTime > 0 && row.ExpiryTime <= now)
		if row.DepletionAction == "throttle" && depleted {
			if mins, ok := periodMinutes[row.DepletionPeriod]; ok && row.DepletionPeriodGB > 0 {
				row.PlanQuotaGB, row.PlanMinutes = row.DepletionPeriodGB, mins
				row.PlanAction, row.PlanSpeed = "throttle", row.DepletionSpeed
			}
		} else if m.plan.minutes > 0 {
			row.PlanQuotaGB, row.PlanMinutes = m.plan.quotaGB, m.plan.minutes
			row.PlanAction, row.PlanSpeed = m.plan.action, m.plan.speed
		}
		if lim, ok := throttleStateFrom(row, now).EffectiveLimit(); ok {
			limits[email] = lim
		}
	}
	return limits
}

// injectThrottling appends the throttle-egress inbound, one socks outbound per
// currently limited client, and one prepended per-user routing rule. Rules go
// to the front so quota enforcement wins over the admin's own matching rules.
// The whole injection is skipped when the relay is not listening (port held by
// something else) or the fixed loopback ports collide with a real inbound.
func injectThrottling(cfg *xray.Config, limits map[string]throttle.Limit) {
	if len(limits) == 0 {
		return
	}
	if !throttle.Default.RelayHealthy() {
		logger.Warning("throttle: relay is not listening, skipping injection")
		return
	}
	for i := range cfg.InboundConfigs {
		port := cfg.InboundConfigs[i].Port
		tag := cfg.InboundConfigs[i].Tag
		if port == throttle.RelayTCPPort && tag != ThrottleEgressInboundTag {
			logger.Warning("throttle: port [", port, "] is used by inbound [", tag, "], skipping injection")
			return
		}
		if port == throttle.EgressInboundPort && tag != ThrottleEgressInboundTag {
			logger.Warning("throttle: port [", port, "] is used by inbound [", tag, "], skipping injection")
			return
		}
		if tag == ThrottleEgressInboundTag {
			logger.Warning("throttle: inbound tag [", ThrottleEgressInboundTag, "] already exists, skipping injection")
			return
		}
	}

	routing := map[string]any{}
	if len(cfg.RouterConfig) > 0 {
		if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
			logger.Warning("throttle: routing section is unparsable, skipping injection:", err)
			return
		}
	}
	rules, _ := routing["rules"].([]any)

	// Sorted so the generated rules/outbounds are byte-stable between runs;
	// map iteration order would otherwise make Equals/hot-diff see changes.
	emails := slices.Sorted(maps.Keys(limits))
	newRules := make([]any, 0, len(emails))
	newOutbounds := make([]any, 0, len(emails))
	for _, email := range emails {
		tag := ThrottleOutboundTag(email)
		newOutbounds = append(newOutbounds, map[string]any{
			"tag":      tag,
			"protocol": "socks",
			"settings": map[string]any{
				"servers": []any{map[string]any{
					"address": "127.0.0.1",
					"port":    throttle.RelayTCPPort,
					"users":   []any{map[string]any{"user": email, "pass": "3x-ui-throttle"}},
				}},
			},
		})
		newRules = append(newRules, map[string]any{
			"type":        "field",
			"user":        []string{email},
			"network":     "tcp,udp",
			"outboundTag": tag,
		})
	}
	routing["rules"] = append(newRules, rules...)

	routingJSON, err := json.Marshal(routing)
	if err != nil {
		logger.Warning("throttle: failed to rebuild routing section, skipping injection:", err)
		return
	}

	var outbounds []any
	if len(cfg.OutboundConfigs) > 0 {
		if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
			logger.Warning("throttle: outbounds section is unparsable, skipping injection:", err)
			return
		}
	}
	outbounds = append(outbounds, newOutbounds...)
	outboundsJSON, err := json.Marshal(outbounds)
	if err != nil {
		logger.Warning("throttle: failed to rebuild outbounds, skipping injection:", err)
		return
	}

	cfg.RouterConfig = json_util.RawMessage(routingJSON)
	cfg.OutboundConfigs = json_util.RawMessage(outboundsJSON)
	cfg.InboundConfigs = append(cfg.InboundConfigs, xray.InboundConfig{
		Listen:   json_util.RawMessage(`"127.0.0.1"`),
		Port:     throttle.EgressInboundPort,
		Protocol: "socks",
		Settings: json_util.RawMessage(`{"auth":"noauth","udp":true}`),
		Tag:      ThrottleEgressInboundTag,
	})
}

// appliedThrottleSet tracks which limited-client set the running core was last
// reconciled with, so per-tick SyncThrottling calls are no-ops while the set
// is stable. Rate VALUE changes never touch Xray — the relay applies them live.
var throttleSync = struct {
	mu      sync.Mutex
	applied bool
	set     map[string]struct{}
}{}

// SyncThrottling aligns the relay's rate buckets with the DB and, when the set
// of limited clients changed, reconciles the core (hot apply, no restart).
// While the core is down it only retunes the relay; the start path injects the
// Xray side through the generated config.
func (s *XrayService) SyncThrottling() {
	limits := s.inboundService.ThrottleLimits()
	throttle.Default.SetLimits(limits)
	if !s.IsXrayRunning() {
		return
	}

	set := make(map[string]struct{}, len(limits))
	for email := range limits {
		set[email] = struct{}{}
	}
	throttleSync.mu.Lock()
	if throttleSync.applied && sameEmailSet(set, throttleSync.set) {
		throttleSync.mu.Unlock()
		return
	}
	throttleSync.mu.Unlock()

	if err := s.RestartXray(false); err != nil {
		// Left unapplied: the next tick retries until the reconcile succeeds.
		logger.Warning("throttle: reconcile xray failed:", err)
		return
	}
	throttleSync.mu.Lock()
	throttleSync.applied = true
	throttleSync.set = set
	throttleSync.mu.Unlock()
}

func sameEmailSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}
