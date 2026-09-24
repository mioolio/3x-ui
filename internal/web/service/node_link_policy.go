package service

import (
	"context"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
)

type joinedLinkPolicy struct {
	model.ClientInbound
	Email string `gorm:"column:email"`
}

func loadInboundLinkPolicies(db *gorm.DB, inboundID int) ([]model.ClientLinkPolicy, error) {
	var rows []joinedLinkPolicy
	if err := db.Table("client_inbounds").
		Select("client_inbounds.*, clients.email AS email").
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("client_inbounds.inbound_id = ?", inboundID).
		Order("clients.id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	policies := make([]model.ClientLinkPolicy, 0, len(rows))
	for _, row := range rows {
		action, multiplier := row.WindowExhaustAction, row.WindowOverageMultiplierBps
		if action == "" {
			action = "stop"
		}
		if multiplier == 0 {
			multiplier = 10000
		}
		policies = append(policies, model.ClientLinkPolicy{
			Email: row.Email, SpeedLimitKbps: row.SpeedLimitKbps,
			SpeedLimitUpKbps: row.SpeedLimitUpKbps, SpeedLimitDownKbps: row.SpeedLimitDownKbps,
			WindowQuotaBytes: row.WindowQuotaBytes, WindowHours: row.WindowHours, WindowMode: row.WindowMode,
			WindowExhaustAction: action, WindowExhaustUpKbps: row.WindowExhaustUpKbps,
			WindowExhaustDownKbps: row.WindowExhaustDownKbps, WindowOverageMultiplierBps: multiplier,
		})
	}
	return policies, nil
}

func defaultLinkPolicy(p model.ClientLinkPolicy) bool {
	if p.SpeedLimitKbps != 0 || p.SpeedLimitUpKbps != nil && *p.SpeedLimitUpKbps != 0 ||
		p.SpeedLimitDownKbps != nil && *p.SpeedLimitDownKbps != 0 {
		return false
	}
	return defaultWindowPolicy(InboundWindowQuota{
		QuotaBytes:                 p.WindowQuotaBytes,
		WindowExhaustAction:        &p.WindowExhaustAction,
		WindowExhaustUpKbps:        &p.WindowExhaustUpKbps,
		WindowExhaustDownKbps:      &p.WindowExhaustDownKbps,
		WindowOverageMultiplierBps: &p.WindowOverageMultiplierBps,
	})
}

// A reconciliation pushes all links, including zero values, so a recovered
// node cannot keep an old per-link restriction after it was cleared centrally.
func (s *InboundService) syncNodeInboundLinkPolicies(ctx context.Context, rt *runtime.Remote, ib *model.Inbound) error {
	policies, err := loadInboundLinkPolicies(database.GetDB(), ib.Id)
	if err != nil {
		return err
	}
	if len(policies) == 0 {
		return nil
	}
	err = rt.SyncInboundLinkPolicies(ctx, ib, policies)
	if err == nil {
		return nil
	}
	legacyDefault := true
	for _, policy := range policies {
		if !defaultLinkPolicy(policy) {
			legacyDefault = false
			break
		}
	}
	return nodePolicyPushFailure(ib, fmt.Sprintf("入站 %d 的关联客户端策略", ib.Id), err, legacyDefault)
}

// SetInboundLinkPolicies atomically replaces all attached link policies on a
// node. The set of emails must match the already reconciled inbound links.
func (s *ClientService) SetInboundLinkPolicies(inboundID int, policies []model.ClientLinkPolicy) error {
	if inboundID <= 0 {
		return fmt.Errorf("invalid inbound id")
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var rows []joinedLinkPolicy
		if err := tx.Table("client_inbounds").
			Select("client_inbounds.client_id, client_inbounds.inbound_id, clients.email AS email").
			Joins("JOIN clients ON clients.id = client_inbounds.client_id").
			Where("client_inbounds.inbound_id = ?", inboundID).Scan(&rows).Error; err != nil {
			return err
		}
		attached := make(map[string]int, len(rows))
		for _, row := range rows {
			attached[row.Email] = row.ClientId
		}
		if len(attached) != len(policies) {
			return fmt.Errorf("inbound %d link policy set does not match attached clients", inboundID)
		}
		seen := make(map[string]bool, len(policies))
		for _, p := range policies {
			clientID, ok := attached[p.Email]
			if !ok || seen[p.Email] {
				return fmt.Errorf("client %q is not uniquely attached to inbound %d", p.Email, inboundID)
			}
			seen[p.Email] = true
			if p.SpeedLimitKbps < 0 || p.SpeedLimitKbps > maxSpeedLimitKbps ||
				p.WindowQuotaBytes < 0 || p.WindowHours < 0 || p.WindowHours > 8760 ||
				(p.WindowQuotaBytes > 0 && p.WindowHours == 0) ||
				(p.WindowMode != "" && p.WindowMode != "fixed" && p.WindowMode != "rolling") {
				return fmt.Errorf("invalid policy for client %q", p.Email)
			}
			if err := validateDirectionalRate("link", p.SpeedLimitUpKbps, p.SpeedLimitDownKbps); err != nil {
				return err
			}
			action, multiplier, mode := p.WindowExhaustAction, p.WindowOverageMultiplierBps, p.WindowMode
			if action == "" {
				action = "stop"
			}
			if multiplier == 0 {
				multiplier = 10000
			}
			if mode == "" {
				mode = "fixed"
			}
			if (action != "stop" && action != "throttle") || p.WindowExhaustUpKbps < 0 || p.WindowExhaustUpKbps > maxSpeedLimitKbps ||
				p.WindowExhaustDownKbps < 0 || p.WindowExhaustDownKbps > maxSpeedLimitKbps ||
				(action == "throttle" && (p.WindowExhaustUpKbps == 0 || p.WindowExhaustDownKbps == 0)) ||
				multiplier < 10000 || multiplier > 1000000 {
				return fmt.Errorf("invalid window exhaust policy for client %q", p.Email)
			}
			if err := tx.Model(&model.ClientInbound{}).
				Where("client_id = ? AND inbound_id = ?", clientID, inboundID).
				Updates(map[string]any{
					"speed_limit_kbps": p.SpeedLimitKbps, "speed_limit_up_kbps": p.SpeedLimitUpKbps,
					"speed_limit_down_kbps": p.SpeedLimitDownKbps,
					"window_quota_bytes":    p.WindowQuotaBytes, "window_hours": p.WindowHours, "window_mode": mode,
					"window_exhaust_action": action, "window_exhaust_up_kbps": p.WindowExhaustUpKbps,
					"window_exhaust_down_kbps": p.WindowExhaustDownKbps, "window_overage_multiplier_bps": multiplier,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
