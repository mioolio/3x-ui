package service

import (
	"math"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

// policyQuotaRule is read by the bundled Xray core. Window quotas count
// physical bytes; total quotas count physical bytes plus multiplier debits.
type policyQuotaRule struct {
	Remaining     int64  `json:"remaining"`
	Epoch         int64  `json:"epoch"`
	Action        string `json:"action"`
	UpKbps        int64  `json:"upKbps"`
	DownKbps      int64  `json:"downKbps"`
	MultiplierBps int    `json:"multiplierBps"`
}

type policyGraceRule struct {
	StartsAt  int64 `json:"startsAt"`
	EndsAt    int64 `json:"endsAt"`
	Remaining int64 `json:"remaining"`
	Epoch     int64 `json:"epoch"`
	UpKbps    int64 `json:"upKbps"`
	DownKbps  int64 `json:"downKbps"`
}

func effectivePolicyAction(action string) string {
	if action == "throttle" {
		return action
	}
	return "stop"
}

func policyUsedBytes(traffic *xray.ClientTraffic) (physical, charged int64) {
	if traffic == nil {
		return 0, 0
	}
	physical = saturatingPositiveSum(traffic.Up, traffic.Down)
	charged = saturatingPositiveSum(physical, traffic.ChargeExtraBytes)
	if traffic.ChargeDiscountBytes >= charged {
		charged = 0
	} else if traffic.ChargeDiscountBytes > 0 {
		charged -= traffic.ChargeDiscountBytes
	}
	return physical, charged
}

func saturatingPositiveSum(a, b int64) int64 {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// BuildEnforcementPolicy assembles the quota and expiry rules from this
// panel's own database. On a node, a fresh master-pushed global traffic row
// raises the physical and charged totals without contaminating its local
// counters or the master's delta baseline.
func BuildEnforcementPolicy(db *gorm.DB, now time.Time) (map[string]policyQuotaRule, map[string]policyQuotaRule, map[string]policyGraceRule, error) {
	totalQuotas := make(map[string]policyQuotaRule)
	windowQuotas := make(map[string]policyQuotaRule)
	grace := make(map[string]policyGraceRule)
	if db == nil {
		return totalQuotas, windowQuotas, grace, nil
	}
	var clients []model.ClientRecord
	if err := db.Find(&clients).Error; err != nil {
		return nil, nil, nil, err
	}
	if len(clients) == 0 {
		return totalQuotas, windowQuotas, grace, nil
	}
	var traffics []*xray.ClientTraffic
	if err := db.Find(&traffics).Error; err != nil {
		return nil, nil, nil, err
	}
	overlayGlobalTraffic(db, traffics)
	trafficByEmail := make(map[string]*xray.ClientTraffic, len(traffics))
	for _, traffic := range traffics {
		if traffic != nil {
			trafficByEmail[traffic.Email] = traffic
		}
	}
	clientByID := make(map[int]model.ClientRecord, len(clients))
	for _, client := range clients {
		clientByID[client.Id] = client
		if !client.Enable || client.Email == "" {
			continue
		}
		traffic := trafficByEmail[client.Email]
		physical, charged := policyUsedBytes(traffic)
		if client.TotalGB > 0 {
			epoch := int64(0)
			if traffic != nil {
				epoch = traffic.QuotaEpoch
			}
			totalQuotas[client.Email] = policyQuotaRule{
				Remaining:     client.TotalGB - charged,
				Epoch:         epoch,
				Action:        effectivePolicyAction(client.TotalExhaustAction),
				UpKbps:        client.TotalExhaustUpKbps,
				DownKbps:      client.TotalExhaustDownKbps,
				MultiplierBps: model.EffectiveOverageMultiplierBps(client.TotalOverageMultiplierBps),
			}
		}
		if client.WindowQuotaBytes > 0 && client.WindowHours > 0 {
			status, err := WindowQuotaStatus(db, client.Id, 0, client.WindowQuotaBytes, client.WindowHours, client.WindowMode, now)
			if err != nil {
				return nil, nil, nil, err
			}
			windowQuotas[client.Email] = policyQuotaRule{
				Remaining:     status.RemainingBytes,
				Epoch:         quotaEpoch(status),
				Action:        effectivePolicyAction(client.WindowExhaustAction),
				UpKbps:        client.WindowExhaustUpKbps,
				DownKbps:      client.WindowExhaustDownKbps,
				MultiplierBps: model.EffectiveOverageMultiplierBps(client.WindowOverageMultiplierBps),
			}
		}
		if client.ExpiryTime <= 0 {
			continue
		}
		endsAt := client.ExpiryTime
		remaining := int64(0)
		if client.GraceHours > 0 {
			endsAt += int64(client.GraceHours) * int64(time.Hour/time.Millisecond)
			if client.GraceQuotaBytes == 0 {
				remaining = math.MaxInt64
			} else {
				baseline := physical
				if traffic != nil && traffic.GraceBaselineExpiry == client.ExpiryTime {
					baseline = traffic.GraceBaselineBytes
				}
				remaining = client.GraceQuotaBytes - max(0, physical-baseline)
			}
		}
		graceEpoch := client.ExpiryTime
		if traffic != nil {
			graceEpoch ^= traffic.QuotaEpoch
		}
		grace[client.Email] = policyGraceRule{
			StartsAt:  client.ExpiryTime,
			EndsAt:    endsAt,
			Remaining: remaining,
			Epoch:     graceEpoch,
			UpKbps:    client.GraceUpKbps,
			DownKbps:  client.GraceDownKbps,
		}
	}
	var inbounds []model.Inbound
	if err := db.Select("id", "tag").Where("node_id IS NULL").Find(&inbounds).Error; err != nil {
		return nil, nil, nil, err
	}
	tagByID := make(map[int]string, len(inbounds))
	for _, inbound := range inbounds {
		tagByID[inbound.Id] = inbound.Tag
	}
	var links []model.ClientInbound
	if err := db.Where("window_quota_bytes > 0 AND window_hours > 0").Find(&links).Error; err != nil {
		return nil, nil, nil, err
	}
	for _, link := range links {
		client, ok := clientByID[link.ClientId]
		tag := tagByID[link.InboundId]
		if !ok || !client.Enable || tag == "" {
			continue
		}
		status, err := WindowQuotaStatus(db, link.ClientId, link.InboundId, link.WindowQuotaBytes, link.WindowHours, link.WindowMode, now)
		if err != nil {
			return nil, nil, nil, err
		}
		windowQuotas[tag+"\x00"+client.Email] = policyQuotaRule{
			Remaining:     status.RemainingBytes,
			Epoch:         quotaEpoch(status),
			Action:        effectivePolicyAction(link.WindowExhaustAction),
			UpKbps:        link.WindowExhaustUpKbps,
			DownKbps:      link.WindowExhaustDownKbps,
			MultiplierBps: model.EffectiveOverageMultiplierBps(link.WindowOverageMultiplierBps),
		}
	}
	return totalQuotas, windowQuotas, grace, nil
}
