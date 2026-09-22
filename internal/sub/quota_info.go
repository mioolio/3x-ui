package sub

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// QuotaInfo carries the resolved per-client quota configuration and live
// fence state for the subscription page's quota tab. Zero quota fields mean
// that layer is not configured.
type QuotaInfo struct {
	WindowQuota   int64  `json:"windowQuota"`
	WindowUsed    int64  `json:"windowUsed"`
	WindowEnd     int64  `json:"windowEnd"` // unix seconds of the running window's rollover
	WindowMinutes int    `json:"windowMinutes"`
	PlanPeriod    string `json:"planPeriod"` // daily / weekly / monthly, empty = off
	PlanQuota     int64  `json:"planQuota"`
	PeriodUsed    int64  `json:"periodUsed"`
	PlanEnd       int64  `json:"planEnd"` // unix seconds of the running period's rollover
	// Effective rate caps in Kbps (0 = unlimited): the strictest of the
	// always-on limit and any currently active throttle.
	SpeedUp        int   `json:"speedUp"`
	SpeedDown      int   `json:"speedDown"`
	ThrottledSince int64 `json:"throttledSince"`
	TotalQuota     int64 `json:"totalQuota"`
	Used           int64 `json:"used"`
	HistoryUsed    int64 `json:"historyUsed"`
	Expiry         int64 `json:"expiry"`
}

// periodMinutes mirrors service.periodMinutes; kept local so the sub server
// does not import the panel's service package.
var periodMinutes = map[string]int{
	"daily":   1440,
	"weekly":  10080,
	"monthly": 43200,
}

func strictestKbps(a, b int) int {
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

// QuotaDetails resolves the quota configuration for a subscription: the
// client's own window/period settings win over the inbound's plan, and the
// strictest (smallest quota) inbound plan applies across the subscriber's
// inbounds. Fence clocks and usage come from the aggregated traffic row.
func (s *SubService) QuotaDetails(subId string, traffic xray.ClientTraffic) QuotaInfo {
	nowMs := time.Now().UnixMilli()
	nowSec := nowMs / 1000
	info := QuotaInfo{
		WindowUsed:     traffic.WindowUsed,
		PeriodUsed:     traffic.PeriodUsed,
		ThrottledSince: traffic.ThrottledSince,
		TotalQuota:     traffic.Total,
		Used:           traffic.Up + traffic.Down,
		HistoryUsed:    traffic.HistoryUp + traffic.HistoryDown,
		Expiry:         traffic.ExpiryTime / 1000,
	}

	db := database.GetDB()
	if db == nil {
		return info
	}

	var records []model.ClientRecord
	if err := db.Model(&model.ClientRecord{}).
		Where("sub_id = ?", subId).
		Find(&records).Error; err != nil {
		records = nil
	}

	var inbounds []model.Inbound
	if err := db.Model(model.Inbound{}).
		Where(`id in (
			SELECT DISTINCT inbounds.id
			FROM inbounds
			JOIN client_inbounds ON client_inbounds.inbound_id = inbounds.id
			JOIN clients ON clients.id = client_inbounds.client_id
			WHERE clients.sub_id = ? AND inbounds.node_id IS NULL
		)`, subId).
		Find(&inbounds).Error; err != nil {
		inbounds = nil
	}

	// Client-level config: the first record with each layer wins (a subId
	// normally maps to exactly one client).
	var cWindow, cPeriod *model.ClientRecord
	for i := range records {
		r := &records[i]
		if cWindow == nil && r.WindowMinutes > 0 {
			cWindow = r
		}
		if cPeriod == nil {
			if _, ok := periodMinutes[r.DepletionPeriod]; ok && r.DepletionPeriodGB > 0 {
				cPeriod = r
			}
		}
	}
	// Inbound-level config: the strictest (smallest quota) configured inbound.
	var iWindow, iPeriod *model.Inbound
	for i := range inbounds {
		ib := &inbounds[i]
		if ib.WindowMinutes > 0 && (iWindow == nil || ib.WindowQuotaGB < iWindow.WindowQuotaGB) {
			iWindow = ib
		}
		if _, ok := periodMinutes[ib.PlanPeriod]; ok && ib.PlanQuotaGB > 0 &&
			(iPeriod == nil || ib.PlanQuotaGB < iPeriod.PlanQuotaGB) {
			iPeriod = ib
		}
	}

	// Effective window layer.
	windowMinutes, windowQuota, windowAction, windowSpeed := 0, int64(0), "", 0
	if cWindow != nil {
		windowMinutes, windowQuota = cWindow.WindowMinutes, cWindow.WindowQuotaGB
		windowAction, windowSpeed = cWindow.WindowAction, cWindow.WindowSpeed
	} else if iWindow != nil {
		windowMinutes, windowQuota = iWindow.WindowMinutes, iWindow.WindowQuotaGB
		windowAction, windowSpeed = iWindow.WindowAction, iWindow.WindowSpeed
	}
	if windowMinutes > 0 {
		info.WindowQuota, info.WindowMinutes = windowQuota, windowMinutes
		if traffic.WindowStarted > 0 {
			info.WindowEnd = (traffic.WindowStarted + int64(windowMinutes)*60000) / 1000
		}
	}

	// Effective period layer. While depletion-throttled, the client's own
	// periodic cap replaces the inbound's plan (the pipeline enforces the
	// client cap as a disable); the inbound plan applies otherwise.
	planPeriod, planQuota, planAction, planSpeed := "", int64(0), "", 0
	if cPeriod != nil && info.ThrottledSince > 0 {
		planPeriod, planQuota = cPeriod.DepletionPeriod, cPeriod.DepletionPeriodGB
	} else if iPeriod != nil {
		planPeriod, planQuota = iPeriod.PlanPeriod, iPeriod.PlanQuotaGB
		planAction, planSpeed = iPeriod.PlanAction, iPeriod.PlanSpeed
	}
	planMinutes := 0
	if planPeriod != "" {
		info.PlanPeriod, info.PlanQuota = planPeriod, planQuota
		planMinutes = periodMinutes[planPeriod]
		if traffic.PeriodStarted > 0 {
			info.PlanEnd = (traffic.PeriodStarted + int64(planMinutes)*60000) / 1000
		}
	}

	// Effective caps: always-on limit, then any throttle active right now.
	var speedUp, speedDown int
	depleted := (traffic.Total > 0 && traffic.Up+traffic.Down >= traffic.Total) ||
		(traffic.ExpiryTime > 0 && traffic.ExpiryTime <= nowMs)
	throttle := 0
	for i := range records {
		speedUp, speedDown = records[i].SpeedLimitUp, records[i].SpeedLimitDown
		if depleted && records[i].DepletionAction == "throttle" {
			throttle = strictestKbps(throttle, records[i].DepletionSpeed)
		}
		break
	}
	if windowAction == "throttle" && windowMinutes > 0 && traffic.WindowStarted > 0 &&
		nowSec < info.WindowEnd && traffic.WindowUsed > windowQuota {
		throttle = strictestKbps(throttle, windowSpeed)
	}
	if planAction == "throttle" && planMinutes > 0 && traffic.PeriodStarted > 0 &&
		nowSec < info.PlanEnd && traffic.PeriodUsed > planQuota {
		throttle = strictestKbps(throttle, planSpeed)
	}
	info.SpeedUp = strictestKbps(speedUp, throttle)
	info.SpeedDown = strictestKbps(speedDown, throttle)
	return info
}
