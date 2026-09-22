package service

import (
	"encoding/json"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

// windowDelta is one poll's per-email traffic increment, fed by the Xray
// counters. Zero-delta emails may be absent from the map.
func windowDeltas(clientTraffics []*xray.ClientTraffic) map[string]int64 {
	deltas := make(map[string]int64, len(clientTraffics))
	for _, ct := range clientTraffics {
		if ct == nil || ct.Email == "" {
			continue
		}
		deltas[ct.Email] += ct.Up + ct.Down
	}
	return deltas
}

// periodMinutes maps a plan period to its fence length in minutes. Fences
// align to each client's first use rather than the calendar: first use opens
// the fence, it rolls over every period.
var periodMinutes = map[string]int{
	"daily":   1440,
	"weekly":  10080,
	"monthly": 43200,
}

// fenceState is the persisted state of one quota fence (window or period).
type fenceState struct {
	used     int64
	started  int64
	disabled bool
	enable   bool
}

// fenceOutcome is the advanced state plus what happened during the step.
type fenceOutcome struct {
	used     int64
	started  int64
	disabled bool
	violated bool // overrun inside the live fence, needs the action applied
	slid     bool // fence rolled over; a fence-disabled client must be restored
}

// advanceFence moves one quota fence forward with this poll's delta. The
// fence opens on the first byte (started==0), rolls when now passes its end,
// and flags a violation while the rolled-in usage exceeds the quota.
func advanceFence(st fenceState, delta, quota int64, minutes int, now int64) fenceOutcome {
	out := fenceOutcome{used: st.used, started: st.started, disabled: st.disabled, violated: st.disabled}
	end := int64(0)
	if st.started > 0 {
		end = st.started + int64(minutes)*60000
	}
	switch {
	case st.started <= 0:
		if delta <= 0 {
			return out
		}
		out.started, out.used = now, delta
	case now >= end:
		out.slid = st.disabled || st.used > quota
		out.started, out.used = now, delta
		out.disabled = false
	default:
		out.used = st.used + delta
	}
	if !out.disabled && out.started > 0 && now < out.started+int64(minutes)*60000 &&
		out.used > quota && st.enable {
		out.violated = true
	}
	return out
}

// effectivePlan is the resolved per-client quota configuration: the client's
// own setting wins over the inbound's plan; across several inbounds the
// strictest (smallest quota) non-empty plan applies.
type effectivePlan struct {
	quotaGB int64
	minutes int
	action  string
	speed   int
}

func strictestPlan(a, b effectivePlan) effectivePlan {
	if a.minutes == 0 {
		return b
	}
	if b.minutes == 0 {
		return a
	}
	if b.quotaGB < a.quotaGB {
		return b
	}
	return a
}

// windowRow is one (client × inbound) row carrying both levels' window
// configuration plus the live fence state.
type windowRow struct {
	Email          string
	CWindowQuota   int64
	CWindowMinutes int
	CWindowAction  string
	CWindowSpeed   int
	IWindowQuota   int64
	IWindowMinutes int
	IWindowAction  string
	IWindowSpeed   int
	WindowUsed     int64
	WindowStarted  int64
	WindowDisabled bool
	TEnable        bool
}

func (r windowRow) effective() effectivePlan {
	client := effectivePlan{quotaGB: r.CWindowQuota, minutes: r.CWindowMinutes, action: r.CWindowAction, speed: r.CWindowSpeed}
	inbound := effectivePlan{quotaGB: r.IWindowQuota, minutes: r.IWindowMinutes, action: r.IWindowAction, speed: r.IWindowSpeed}
	if client.minutes > 0 {
		return client
	}
	return inbound
}

// planRow is one (client × inbound) row carrying the depletion-throttle
// configuration, the inbound's periodic plan, and the period fence state.
type planRow struct {
	Email              string
	DepletionAction    string
	DepletionSpeed     int
	DepletionGraceDays int
	DepletionPeriod    string
	DepletionPeriodGB  int64
	PlanPeriod         string
	PlanQuotaGB        int64
	PlanAction         string
	PlanSpeed          int
	Up                 int64
	Down               int64
	Total              int64
	ExpiryTime         int64
	ThrottledSince     int64
	PeriodUsed         int64
	PeriodStarted      int64
	PeriodDisabled     bool
	TEnable            bool
}

type throttleAcc struct {
	// client-level (identical across the email's rows)
	graceDays          int
	depletionPeriod    string
	depletionPeriodGB  int64
	exhausted          bool
	throttled          bool
	needsThrottledStamp bool
	since              int64
	// inbound-level plan: strictest non-empty across the client's inbounds
	plan effectivePlan
	// period fence state
	state fenceState
}

// enforceQuotaWindows advances every quota fence — per-client and inbound-level
// window quotas, then depletion grace and periodic plans — and applies the
// configured actions. Runs inside the lifecycle transaction, before
// disableInvalidClients, so a fence-disabled client never has its enable=true
// row scanned by the depleted predicate.
func (s *InboundService) enforceQuotaWindows(tx *gorm.DB, mutationBatch *trafficMutationBatch, deltas map[string]int64) error {
	if err := s.enforceWindowQuotas(tx, mutationBatch, deltas); err != nil {
		return err
	}
	return s.enforceDepletionPeriods(tx, mutationBatch, deltas)
}

func (s *InboundService) enforceWindowQuotas(tx *gorm.DB, mutationBatch *trafficMutationBatch, deltas map[string]int64) error {
	var rows []windowRow
	err := tx.Table("clients").
		Select(`clients.email AS email,
			COALESCE(clients.window_quota_gb, 0) AS c_window_quota,
			COALESCE(clients.window_minutes, 0) AS c_window_minutes,
			COALESCE(clients.window_action, '') AS c_window_action,
			COALESCE(clients.window_speed, 0) AS c_window_speed,
			COALESCE(inbounds.window_quota_gb, 0) AS i_window_quota,
			COALESCE(inbounds.window_minutes, 0) AS i_window_minutes,
			COALESCE(inbounds.window_action, '') AS i_window_action,
			COALESCE(inbounds.window_speed, 0) AS i_window_speed,
			COALESCE(client_traffics.window_used, 0) AS window_used,
			COALESCE(client_traffics.window_started, 0) AS window_started,
			COALESCE(client_traffics.window_disabled, false) AS window_disabled,
			client_traffics.enable AS t_enable`).
		Joins("JOIN client_inbounds ON client_inbounds.client_id = clients.id").
		Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id AND inbounds.node_id IS NULL").
		Joins("JOIN client_traffics ON client_traffics.email = clients.email").
		Where("COALESCE(clients.window_minutes, 0) > 0 OR COALESCE(inbounds.window_minutes, 0) > 0").
		Find(&rows).Error
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()

	plans := make(map[string]effectivePlan, len(rows))
	states := make(map[string]fenceState, len(rows))
	for _, r := range rows {
		plan := r.effective()
		if plan.minutes <= 0 {
			continue
		}
		if cur, ok := plans[r.Email]; ok {
			plans[r.Email] = strictestPlan(cur, plan)
		} else {
			plans[r.Email] = plan
		}
		states[r.Email] = fenceState{used: r.WindowUsed, started: r.WindowStarted, disabled: r.WindowDisabled, enable: r.TEnable}
	}

	var violated, slid []string
	for email, plan := range plans {
		out := advanceFence(states[email], deltas[email], plan.quotaGB, plan.minutes, now)
		if out.slid {
			// Restore while the stale disabled flag is still in the DB so
			// only fence-disabled clients are resurrected, never ones an
			// operator switched off by hand.
			slid = append(slid, email)
		}
		if out.violated && plan.action == "disable" {
			violated = append(violated, email)
		}
		if err := tx.Model(xray.ClientTraffic{}).
			Where("email = ?", email).
			Updates(map[string]any{"window_used": out.used, "window_started": out.started, "window_disabled": out.disabled}).Error; err != nil {
			return err
		}
	}
	if len(slid) > 0 {
		if err := s.fenceRestoreClients(tx, mutationBatch, slid, "window"); err != nil {
			return err
		}
		logger.Infof("%v clients restored after quota window slid", len(slid))
	}
	if len(violated) > 0 {
		if err := s.fenceDisableClients(tx, mutationBatch, violated); err != nil {
			return err
		}
		logger.Infof("%v clients disabled by quota window", len(violated))
	}
	return nil
}

// enforceDepletionPeriods maintains the depletion-throttle bookkeeping: the
// throttled-since timestamp (grace clock) and the periodic traffic caps that
// apply while a client is throttled (client-level DepletionPeriod) or always
// (the inbound's plan). Grace-expired clients are NOT disabled here — dropping
// the throttle exclusion in disableInvalidClients lets the existing depleted
// path disable them, keeping one disable write path.
func (s *InboundService) enforceDepletionPeriods(tx *gorm.DB, mutationBatch *trafficMutationBatch, deltas map[string]int64) error {
	var rows []planRow
	err := tx.Table("clients").
		Select(`clients.email AS email,
			COALESCE(clients.depletion_action, '') AS depletion_action,
			COALESCE(clients.depletion_speed, 0) AS depletion_speed,
			COALESCE(clients.depletion_grace_days, 0) AS depletion_grace_days,
			COALESCE(clients.depletion_period, '') AS depletion_period,
			COALESCE(clients.depletion_period_gb, 0) AS depletion_period_gb,
			COALESCE(inbounds.plan_period, '') AS plan_period,
			COALESCE(inbounds.plan_quota_gb, 0) AS plan_quota_gb,
			COALESCE(inbounds.plan_action, '') AS plan_action,
			COALESCE(inbounds.plan_speed, 0) AS plan_speed,
			client_traffics.up AS up,
			client_traffics.down AS down,
			COALESCE(client_traffics.total, 0) AS total,
			COALESCE(client_traffics.expiry_time, 0) AS expiry_time,
			COALESCE(client_traffics.throttled_since, 0) AS throttled_since,
			COALESCE(client_traffics.period_used, 0) AS period_used,
			COALESCE(client_traffics.period_started, 0) AS period_started,
			COALESCE(client_traffics.period_disabled, false) AS period_disabled,
			client_traffics.enable AS t_enable`).
		Joins("JOIN client_inbounds ON client_inbounds.client_id = clients.id").
		Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id AND inbounds.node_id IS NULL").
		Joins("JOIN client_traffics ON client_traffics.email = clients.email").
		Where("clients.depletion_action = ? OR COALESCE(clients.depletion_period, '') <> '' OR COALESCE(inbounds.plan_period, '') <> ''", "throttle").
		Find(&rows).Error
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()

	accs := make(map[string]*throttleAcc, len(rows))
	for i := range rows {
		r := rows[i]
		a := accs[r.Email]
		if a == nil {
			exhausted := (r.Total > 0 && r.Up+r.Down >= r.Total) || (r.ExpiryTime > 0 && r.ExpiryTime <= now)
			a = &throttleAcc{
				graceDays:         r.DepletionGraceDays,
				depletionPeriod:   r.DepletionPeriod,
				depletionPeriodGB: r.DepletionPeriodGB,
				exhausted:         exhausted,
				throttled:         exhausted && r.DepletionAction == "throttle",
				since:             r.ThrottledSince,
				state:             fenceState{used: r.PeriodUsed, started: r.PeriodStarted, disabled: r.PeriodDisabled, enable: r.TEnable},
			}
			a.needsThrottledStamp = a.throttled && a.since == 0
			accs[r.Email] = a
		}
		if mins, ok := periodMinutes[r.PlanPeriod]; ok && r.PlanQuotaGB > 0 {
			plan := effectivePlan{quotaGB: r.PlanQuotaGB, minutes: mins, action: r.PlanAction, speed: r.PlanSpeed}
			if a.plan.minutes > 0 {
				a.plan = strictestPlan(a.plan, plan)
			} else {
				a.plan = plan
			}
		}
	}

	var violated, slid []string
	for email, a := range accs {
		if a.needsThrottledStamp {
			if err := tx.Model(xray.ClientTraffic{}).
				Where("email = ? AND throttled_since = 0", email).
				Update("throttled_since", now).Error; err != nil {
				return err
			}
		}

		// Effective periodic cap: while depletion-throttled the client's own
		// period cap wins over the inbound's always-on plan. Its action is
		// always disable — the cap exists to stop a throttled client from
		// running at the reduced rate forever.
		plan := a.plan
		if a.throttled {
			if mins, ok := periodMinutes[a.depletionPeriod]; ok && a.depletionPeriodGB > 0 {
				plan = effectivePlan{quotaGB: a.depletionPeriodGB, minutes: mins, action: "disable"}
			}
		}
		if plan.minutes <= 0 {
			continue
		}
		out := advanceFence(a.state, deltas[email], plan.quotaGB, plan.minutes, now)
		if out.slid {
			slid = append(slid, email)
		}
		if out.violated {
			if plan.action == "disable" {
				violated = append(violated, email)
			}
		}
		if err := tx.Model(xray.ClientTraffic{}).
			Where("email = ?", email).
			Updates(map[string]any{"period_used": out.used, "period_started": out.started, "period_disabled": out.disabled}).Error; err != nil {
			return err
		}
	}
	if len(slid) > 0 {
		if err := s.fenceRestoreClients(tx, mutationBatch, slid, "period"); err != nil {
			return err
		}
		logger.Infof("%v clients restored after their plan period rolled", len(slid))
	}
	if len(violated) > 0 {
		if err := s.fenceDisableClients(tx, mutationBatch, violated); err != nil {
			return err
		}
		logger.Infof("%v clients disabled by their traffic plan", len(violated))
	}
	return nil
}

// fenceDisableClients flips fence-overrun clients off with the same three-way
// write (settings JSON, clients, client_traffics) and user-removal plans the
// quota disable uses.
func (s *InboundService) fenceDisableClients(tx *gorm.DB, mutationBatch *trafficMutationBatch, emails []string) error {
	type target struct {
		InboundID int  `gorm:"column:inbound_id"`
		NodeID    *int `gorm:"column:node_id"`
		Tag       string
		Email     string
	}
	var targets []target
	err := tx.Raw(`
		SELECT inbounds.id AS inbound_id, inbounds.node_id AS node_id,
		       inbounds.tag AS tag, clients.email AS email
		FROM clients
		JOIN client_inbounds ON client_inbounds.client_id = clients.id
		JOIN inbounds        ON inbounds.id = client_inbounds.inbound_id
		WHERE clients.email IN ?
	`, emails).Scan(&targets).Error
	if err != nil {
		return err
	}

	byInbound := make(map[int][]target)
	for _, t := range targets {
		byInbound[t.InboundID] = append(byInbound[t.InboundID], t)
	}
	for inboundID, group := range byInbound {
		emailSet := make(map[string]struct{}, len(group))
		for _, t := range group {
			emailSet[t.Email] = struct{}{}
		}
		oldInbound, inbound, mErr := s.markClientsDisabledInSettings(tx, inboundID, emailSet)
		if mErr != nil {
			return mErr
		}
		if inbound.NodeID != nil {
			mutationBatch.remotePlans = append(mutationBatch.remotePlans, trafficInboundUpdatePlan{
				oldInbound: *oldInbound, newInbound: *inbound,
			})
			mutationBatch.addNode(*inbound.NodeID)
			continue
		}
		for email := range emailSet {
			mutationBatch.localPlans = append(mutationBatch.localPlans, trafficLocalApplyPlan{
				action: trafficRemoveUser, inbound: *inbound, email: email,
			})
		}
	}

	if err := tx.Model(xray.ClientTraffic{}).
		Where("email IN ?", emails).
		Update("enable", false).Error; err != nil {
		return err
	}
	return tx.Model(&model.ClientRecord{}).
		Where("email IN ?", emails).
		Update("enable", false).Error
}

// fenceRestoreClients re-enables clients whose violating fence rolled away.
// column tells which fence's disabled flag gates the restore — only clients
// that THIS fence switched off are resurrected, never ones an operator
// disabled by hand. Mirrors autoRenewClients' re-enable: settings JSON,
// clients, client_traffics and add-user plans so connections resume without
// a restart.
func (s *InboundService) fenceRestoreClients(tx *gorm.DB, mutationBatch *trafficMutationBatch, emails []string, column string) error {
	if len(emails) == 0 || (column != "window" && column != "period") {
		return nil
	}
	flagCol := column + "_disabled"

	// Only these emails still carry the fence's disabled flag; a manual
	// disable never sets it, so this is what protects operator intent.
	var flagged []string
	if err := tx.Model(xray.ClientTraffic{}).
		Where("email IN ? AND "+flagCol+" = ?", emails, true).
		Pluck("email", &flagged).Error; err != nil {
		return err
	}
	if len(flagged) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()

	var inboundIds []int
	if err := tx.Table("client_inbounds").
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("clients.email IN ?", flagged).
		Distinct().
		Pluck("client_inbounds.inbound_id", &inboundIds).Error; err != nil {
		return err
	}
	if len(inboundIds) == 0 {
		return nil
	}
	var inbounds []*model.Inbound
	if err := tx.Model(model.Inbound{}).Where("id IN ?", inboundIds).Find(&inbounds).Error; err != nil {
		return err
	}

	restoreSet := make(map[string]struct{}, len(flagged))
	for _, email := range flagged {
		restoreSet[email] = struct{}{}
	}
	type addOp struct {
		inbound model.Inbound
		client  map[string]any
	}
	var clientsToAdd []addOp

	for _, inbound := range inbounds {
		settings := map[string]any{}
		_ = json.Unmarshal([]byte(inbound.Settings), &settings)
		clients, _ := settings["clients"].([]any)
		if len(clients) == 0 {
			continue
		}
		cipher := ""
		if inbound.Protocol == model.Shadowsocks {
			cipher, _ = settings["method"].(string)
		}
		mutated := false
		for client_index := range clients {
			c, ok := clients[client_index].(map[string]any)
			if !ok {
				continue
			}
			email, _ := c["email"].(string)
			if _, hit := restoreSet[email]; !hit {
				continue
			}
			if cur, _ := c["enable"].(bool); !cur {
				c["enable"] = true
				c["updated_at"] = now
				mutated = true
				clientsToAdd = append(clientsToAdd, addOp{
					inbound: *inbound, client: apiUserFromClient(c, cipher),
				})
			}
			clients[client_index] = any(c)
		}
		if mutated {
			settings["clients"] = clients
			newSettings, err := json.MarshalIndent(settings, "", "  ")
			if err != nil {
				return err
			}
			inbound.Settings = string(newSettings)
		}
	}
	if err := tx.Save(inbounds).Error; err != nil {
		return err
	}

	if err := tx.Model(xray.ClientTraffic{}).
		Where("email IN ?", flagged).
		Updates(map[string]any{"enable": true, flagCol: false}).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.ClientRecord{}).
		Where("email IN ?", flagged).
		Update("enable", true).Error; err != nil {
		return err
	}

	for _, op := range clientsToAdd {
		if op.inbound.NodeID != nil {
			mutationBatch.addNode(*op.inbound.NodeID)
			continue
		}
		mutationBatch.localPlans = append(mutationBatch.localPlans, trafficLocalApplyPlan{
			action: trafficAddUser, inbound: op.inbound, client: op.client,
		})
	}
	return nil
}

// clearQuotaWindowState wipes a client's fence and throttle bookkeeping after
// an operator reset. Lifetime history counters are deliberately kept.
func clearQuotaWindowState(tx *gorm.DB, emails ...string) error {
	if len(emails) == 0 {
		return nil
	}
	return tx.Model(xray.ClientTraffic{}).
		Where("email IN ?", emails).
		Updates(map[string]any{
			"window_used": 0, "window_started": 0, "window_disabled": false,
			"period_used": 0, "period_started": 0, "period_disabled": false,
			"throttled_since": 0,
		}).Error
}
