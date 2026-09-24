package service

import (
	"bytes"
	"encoding/json"
	"os"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type ratePolicy struct {
	Inbounds           map[string]int64           `json:"inbounds"`
	Clients            map[string]int64           `json:"clients"`
	Overrides          map[string]int64           `json:"overrides"`
	InboundDirections  map[string]directionalRate `json:"inboundDirections"`
	InboundMultipliers map[string]int             `json:"inboundMultipliers"`
	ClientDirections   map[string]directionalRate `json:"clientDirections"`
	OverrideDirections map[string]directionalRate `json:"overrideDirections"`
	Blocked            map[string]bool            `json:"blocked"`
	Quotas             map[string]rateQuota       `json:"quotas"`
	TotalQuotas        map[string]policyQuotaRule `json:"totalQuotas"`
	WindowQuotas       map[string]policyQuotaRule `json:"windowQuotas"`
	Grace              map[string]policyGraceRule `json:"grace"`
}

type rateQuota struct {
	Remaining int64 `json:"remaining"`
	Epoch     int64 `json:"epoch"`
}

// RefreshRatePolicy publishes durable limits to the bundled Xray core. The
// file is replaced atomically; a running process picks it up within one second.
func (s *XrayService) RefreshRatePolicy() error {
	db := database.GetDB()
	if db == nil {
		return nil
	}
	var inbounds []model.Inbound
	if err := db.Select("id", "tag", "enable", "speed_limit_kbps", "speed_limit_up_kbps", "speed_limit_down_kbps", "traffic_multiplier_bps").Where("node_id IS NULL").Find(&inbounds).Error; err != nil {
		return err
	}
	var clients []model.ClientRecord
	if err := db.Select("id", "email", "enable", "speed_limit_kbps", "speed_limit_up_kbps", "speed_limit_down_kbps", "window_quota_bytes", "window_hours", "window_mode").Find(&clients).Error; err != nil {
		return err
	}
	var links []model.ClientInbound
	if err := db.Select("client_id", "inbound_id", "speed_limit_kbps", "speed_limit_up_kbps", "speed_limit_down_kbps", "window_quota_bytes", "window_hours", "window_mode").Where("speed_limit_kbps > 0 OR speed_limit_up_kbps IS NOT NULL OR speed_limit_down_kbps IS NOT NULL OR window_quota_bytes > 0").Find(&links).Error; err != nil {
		return err
	}
	policy := ratePolicy{
		Inbounds:           make(map[string]int64),
		Clients:            make(map[string]int64),
		Overrides:          make(map[string]int64),
		InboundDirections:  make(map[string]directionalRate),
		InboundMultipliers: make(map[string]int),
		ClientDirections:   make(map[string]directionalRate),
		OverrideDirections: make(map[string]directionalRate),
		Blocked:            make(map[string]bool),
		Quotas:             make(map[string]rateQuota),
	}
	tags := make(map[int]string, len(inbounds))
	for _, inbound := range inbounds {
		tags[inbound.Id] = inbound.Tag
		if inbound.Enable {
			if factor := model.EffectiveTrafficMultiplierBps(inbound.TrafficMultiplierBps); factor != model.DefaultTrafficMultiplierBps && factor >= 100 && factor <= model.MaxTrafficMultiplierBps {
				policy.InboundMultipliers[inbound.Tag] = factor
			}
			if directional, ok := effectiveDirectionalRate(inbound.SpeedLimitKbps, inbound.SpeedLimitUpKbps, inbound.SpeedLimitDownKbps); ok {
				policy.InboundDirections[inbound.Tag] = directional
			} else if inbound.SpeedLimitKbps > 0 {
				policy.Inbounds[inbound.Tag] = inbound.SpeedLimitKbps
			}
		}
	}
	emails := make(map[int]string, len(clients))
	now := time.Now()
	for _, client := range clients {
		emails[client.Id] = client.Email
		if client.Enable {
			if directional, ok := effectiveDirectionalRate(client.SpeedLimitKbps, client.SpeedLimitUpKbps, client.SpeedLimitDownKbps); ok {
				policy.ClientDirections[client.Email] = directional
			} else if client.SpeedLimitKbps > 0 {
				policy.Clients[client.Email] = client.SpeedLimitKbps
			}
		}
		if client.Enable && client.WindowQuotaBytes > 0 && client.WindowHours > 0 {
			status, err := WindowQuotaStatus(db, client.Id, 0, client.WindowQuotaBytes, client.WindowHours, client.WindowMode, now)
			if err != nil {
				return err
			}
			if status.RemainingBytes == 0 {
				policy.Blocked[client.Email] = true
			}
			policy.Quotas[client.Email] = rateQuota{Remaining: status.RemainingBytes, Epoch: quotaEpoch(status)}
		}
	}
	for _, link := range links {
		tag, okTag := tags[link.InboundId]
		email, okEmail := emails[link.ClientId]
		if okTag && okEmail {
			key := tag + "\x00" + email
			if directional, ok := effectiveDirectionalRate(link.SpeedLimitKbps, link.SpeedLimitUpKbps, link.SpeedLimitDownKbps); ok {
				policy.OverrideDirections[key] = directional
			} else if link.SpeedLimitKbps > 0 {
				policy.Overrides[key] = link.SpeedLimitKbps
			}
		}
		if okTag && okEmail && link.WindowQuotaBytes > 0 && link.WindowHours > 0 {
			status, err := WindowQuotaStatus(db, link.ClientId, link.InboundId, link.WindowQuotaBytes, link.WindowHours, link.WindowMode, now)
			if err != nil {
				return err
			}
			if status.RemainingBytes == 0 {
				policy.Blocked[tag+"\x00"+email] = true
			}
			policy.Quotas[tag+"\x00"+email] = rateQuota{Remaining: status.RemainingBytes, Epoch: quotaEpoch(status)}
		}
	}
	var err error
	policy.TotalQuotas, policy.WindowQuotas, policy.Grace, err = BuildEnforcementPolicy(db, now)
	if err != nil {
		return err
	}
	// The legacy blocked map is evaluated before quota rules in the patched
	// core. Remove only exhausted windows that explicitly allow slow overage.
	for key, rule := range policy.WindowQuotas {
		if rule.Action == "throttle" {
			delete(policy.Blocked, key)
		}
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if prior, err := os.ReadFile(xray.GetRatePolicyPath()); err == nil && bytes.Equal(prior, data) {
		return nil
	}
	return xray.WriteRatePolicyFile(data)
}

func quotaEpoch(status WindowStatus) int64 {
	if status.WindowMode == "rolling" {
		return 0
	}
	return status.WindowStart
}
