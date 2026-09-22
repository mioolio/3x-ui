package sub

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// QuotaInfo carries the resolved per-client quota configuration and live
// fence state for the subscription page's quota tab. Zero quota fields mean
// that layer is not configured.
type QuotaInfo struct {
	WindowQuota    int64  `json:"windowQuota"`
	WindowUsed     int64  `json:"windowUsed"`
	WindowEnd      int64  `json:"windowEnd"` // unix seconds of the running window's rollover
	WindowMinutes  int    `json:"windowMinutes"`
	PlanPeriod     string `json:"planPeriod"` // daily / weekly / monthly, empty = off
	PlanQuota      int64  `json:"planQuota"`
	PeriodUsed     int64  `json:"periodUsed"`
	PlanEnd        int64  `json:"planEnd"` // unix seconds of the running period's rollover
	ThrottledSince int64  `json:"throttledSince"`
	TotalQuota     int64  `json:"totalQuota"`
	Used           int64  `json:"used"`
	HistoryUsed    int64  `json:"historyUsed"`
	Expiry         int64  `json:"expiry"`

	planMinutes int // resolved alongside PlanPeriod; not serialized
}

// periodMinutes mirrors service.periodMinutes; kept local so the sub server
// does not import the panel's service package.
var periodMinutes = map[string]int{
	"daily":   1440,
	"weekly":  10080,
	"monthly": 43200,
}

// QuotaDetails resolves the quota configuration for a subscription: the
// client's own window/period settings win over the inbound's plan, and the
// strictest (smallest quota) inbound plan applies across the subscriber's
// inbounds. Fence clocks come from the aggregated traffic row.
func (s *SubService) QuotaDetails(subId string, traffic xray.ClientTraffic) QuotaInfo {
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
		Find(&records).Error; err == nil {
		for _, r := range records {
			if r.WindowMinutes > 0 {
				info.WindowQuota, info.WindowMinutes = r.WindowQuotaGB, r.WindowMinutes
				break
			}
		}
		for _, r := range records {
			if mins, ok := periodMinutes[r.DepletionPeriod]; ok && r.DepletionPeriodGB > 0 {
				info.PlanPeriod, info.PlanQuota = r.DepletionPeriod, r.DepletionPeriodGB
				info.planMinutes = mins
				break
			}
		}
	}

	if info.WindowMinutes == 0 {
		var inbounds []model.Inbound
		if err := db.Model(model.Inbound{}).
			Where(`id in (
				SELECT DISTINCT inbounds.id
				FROM inbounds
				JOIN client_inbounds ON client_inbounds.inbound_id = inbounds.id
				JOIN clients ON clients.id = client_inbounds.client_id
				WHERE clients.sub_id = ? AND inbounds.node_id IS NULL
			)`, subId).
			Find(&inbounds).Error; err == nil {
			for _, ib := range inbounds {
				if ib.WindowMinutes > 0 {
					if info.WindowMinutes == 0 || ib.WindowQuotaGB < info.WindowQuota {
						info.WindowQuota, info.WindowMinutes = ib.WindowQuotaGB, ib.WindowMinutes
					}
				}
			}
		}
	}

	if info.PlanPeriod == "" {
		var inbounds []model.Inbound
		if err := db.Model(model.Inbound{}).
			Where(`id in (
				SELECT DISTINCT inbounds.id
				FROM inbounds
				JOIN client_inbounds ON client_inbounds.inbound_id = inbounds.id
				JOIN clients ON clients.id = client_inbounds.client_id
				WHERE clients.sub_id = ? AND inbounds.node_id IS NULL
			)`, subId).
			Find(&inbounds).Error; err == nil {
			for _, ib := range inbounds {
				if mins, ok := periodMinutes[ib.PlanPeriod]; ok && ib.PlanQuotaGB > 0 {
					if info.PlanPeriod == "" || ib.PlanQuotaGB < info.PlanQuota {
						info.PlanPeriod, info.PlanQuota = ib.PlanPeriod, ib.PlanQuotaGB
						info.planMinutes = mins
					}
				}
			}
		}
	}

	if info.WindowMinutes > 0 && traffic.WindowStarted > 0 {
		info.WindowEnd = (traffic.WindowStarted + int64(info.WindowMinutes)*60000) / 1000
	}
	if info.PlanPeriod != "" {
		mins := info.planMinutes
		if mins == 0 {
			mins = periodMinutes[info.PlanPeriod]
		}
		if traffic.PeriodStarted > 0 {
			info.PlanEnd = (traffic.PeriodStarted + int64(mins)*60000) / 1000
		}
	}
	return info
}
