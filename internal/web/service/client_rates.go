package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
)

func (s *ClientService) GetInboundWindowStatus(email string, inboundID int) (WindowStatus, error) {
	db := database.GetDB()
	var client model.ClientRecord
	if err := db.Select("id").Where("email = ?", email).First(&client).Error; err != nil {
		return WindowStatus{}, err
	}
	var link model.ClientInbound
	if err := db.Where("client_id = ? AND inbound_id = ?", client.Id, inboundID).First(&link).Error; err != nil {
		return WindowStatus{}, err
	}
	return WindowQuotaStatus(db, client.Id, inboundID, link.WindowQuotaBytes, link.WindowHours, link.WindowMode, time.Now())
}

type InboundWindowQuota struct {
	QuotaBytes                 int64   `json:"quotaBytes"`
	Hours                      int     `json:"hours"`
	Mode                       string  `json:"mode"`
	WindowExhaustAction        *string `json:"windowExhaustAction,omitempty"`
	WindowExhaustUpKbps        *int64  `json:"windowExhaustUpKbps,omitempty"`
	WindowExhaustDownKbps      *int64  `json:"windowExhaustDownKbps,omitempty"`
	WindowOverageMultiplierBps *int    `json:"windowOverageMultiplierBps,omitempty"`
}

func linkedPolicyPushError(inbound *model.Inbound, cause error) error {
	if inbound == nil || inbound.NodeID == nil {
		return cause
	}
	if err := (&NodeService{}).MarkNodeDirty(*inbound.NodeID); err != nil {
		return errors.Join(cause, fmt.Errorf("mark node %d for policy reconciliation: %w", *inbound.NodeID, err))
	}
	return cause
}

func nodePolicyPushFailure(inbound *model.Inbound, policy string, err error, legacyDefault bool) error {
	if err == nil {
		return nil
	}
	if runtime.IsMissingPolicyEndpoint(err) {
		if legacyDefault {
			// A node predating this policy API already enforces the empty/default
			// state. Do not turn basic client creation into a partial failure.
			return nil
		}
		err = fmt.Errorf("节点需升级后才能应用%s (node upgrade required): %w", policy, err)
	}
	return linkedPolicyPushError(inbound, err)
}

func defaultWindowPolicy(q InboundWindowQuota) bool {
	if q.QuotaBytes != 0 {
		return false
	}
	if q.WindowExhaustAction != nil && *q.WindowExhaustAction != "" && *q.WindowExhaustAction != "stop" {
		return false
	}
	if q.WindowExhaustUpKbps != nil && *q.WindowExhaustUpKbps != 0 {
		return false
	}
	if q.WindowExhaustDownKbps != nil && *q.WindowExhaustDownKbps != 0 {
		return false
	}
	return q.WindowOverageMultiplierBps == nil || *q.WindowOverageMultiplierBps == 0 || *q.WindowOverageMultiplierBps == 10000
}

type InboundDirectionalRate struct {
	UpKbps   int64 `json:"upKbps"`
	DownKbps int64 `json:"downKbps"`
}

func (s *ClientService) PushInboundDirectionalRates(inboundSvc *InboundService, email string, rates map[int]InboundDirectionalRate) error {
	for id, rate := range rates {
		ib, err := inboundSvc.GetInbound(id)
		if err != nil {
			return err
		}
		if ib.NodeID == nil {
			continue
		}
		rt, err := inboundSvc.runtimeFor(ib)
		if err != nil {
			return err
		}
		remote, ok := rt.(*runtime.Remote)
		if !ok {
			return fmt.Errorf("inbound %d is not managed by a remote runtime", id)
		}
		ctx, cancel := nodePushContext()
		err = remote.SetClientDirectionalRate(ctx, ib, email, rate.UpKbps, rate.DownKbps)
		cancel()
		if err != nil {
			if runtime.IsMissingPolicyEndpoint(err) && rate.UpKbps == 0 && rate.DownKbps == 0 {
				// Clear a legacy symmetric rate if that API exists. A node older
				// than both policy routes already has the unrestricted default.
				ctx, cancel = nodePushContext()
				fallbackErr := remote.SetClientRate(ctx, ib, email, 0)
				cancel()
				if fallbackErr == nil || runtime.IsMissingPolicyEndpoint(fallbackErr) {
					continue
				}
				return linkedPolicyPushError(ib, fmt.Errorf("clear inbound %d legacy rate: %w", id, fallbackErr))
			}
			return nodePolicyPushFailure(ib, fmt.Sprintf("入站 %d 的上/下行限速", id), fmt.Errorf("push inbound %d directional rate: %w", id, err), false)
		}
	}
	return nil
}

func (s *ClientService) GetInboundDirectionalRates(email string) (map[int]InboundDirectionalRate, error) {
	var rows []model.ClientInbound
	err := database.GetDB().Table("client_inbounds").
		Select("client_inbounds.inbound_id, client_inbounds.speed_limit_kbps, client_inbounds.speed_limit_up_kbps, client_inbounds.speed_limit_down_kbps").
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("clients.email = ?", email).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	rates := make(map[int]InboundDirectionalRate, len(rows))
	for _, row := range rows {
		upKbps, downKbps := row.SpeedLimitKbps, row.SpeedLimitKbps
		if row.SpeedLimitUpKbps != nil {
			upKbps = *row.SpeedLimitUpKbps
		}
		if row.SpeedLimitDownKbps != nil {
			downKbps = *row.SpeedLimitDownKbps
		}
		rates[row.InboundId] = InboundDirectionalRate{UpKbps: upKbps, DownKbps: downKbps}
	}
	return rates, nil
}

func (s *ClientService) SetInboundDirectionalRates(email string, rates map[int]InboundDirectionalRate) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var client model.ClientRecord
		if err := tx.Where("email = ?", email).First(&client).Error; err != nil {
			return err
		}
		var links []model.ClientInbound
		if err := tx.Where("client_id = ?", client.Id).Find(&links).Error; err != nil {
			return err
		}
		attached := make(map[int]bool, len(links))
		for _, link := range links {
			attached[link.InboundId] = true
		}
		for id, rate := range rates {
			if !attached[id] {
				return fmt.Errorf("inbound %d is not attached to client", id)
			}
			if rate.UpKbps < 0 || rate.UpKbps > maxSpeedLimitKbps || rate.DownKbps < 0 || rate.DownKbps > maxSpeedLimitKbps {
				return fmt.Errorf("inbound %d directional rate must be between 0 and %d Kbps", id, maxSpeedLimitKbps)
			}
		}
		for id, rate := range rates {
			if err := tx.Model(&model.ClientInbound{}).
				Where("client_id = ? AND inbound_id = ?", client.Id, id).
				Updates(map[string]any{"speed_limit_kbps": 0, "speed_limit_up_kbps": rate.UpKbps, "speed_limit_down_kbps": rate.DownKbps}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// PushInboundRates applies per-link limits to the node that actually owns each
// inbound. The local policy reads the same rows directly from this database.
func (s *ClientService) PushInboundRates(inboundSvc *InboundService, email string, rates map[int]int64) error {
	for id, kbps := range rates {
		ib, err := inboundSvc.GetInbound(id)
		if err != nil {
			return err
		}
		if ib.NodeID == nil {
			continue
		}
		rt, err := inboundSvc.runtimeFor(ib)
		if err != nil {
			return err
		}
		remote, ok := rt.(*runtime.Remote)
		if !ok {
			return fmt.Errorf("inbound %d is not managed by a remote runtime", id)
		}
		ctx, cancel := nodePushContext()
		err = remote.SetClientRate(ctx, ib, email, kbps)
		cancel()
		if err != nil {
			if result := nodePolicyPushFailure(ib, fmt.Sprintf("入站 %d 的限速", id), fmt.Errorf("push inbound %d rate: %w", id, err), kbps == 0); result != nil {
				return result
			}
		}
	}
	return nil
}

func (s *ClientService) PushInboundWindowQuotas(inboundSvc *InboundService, email string, quotas map[int]InboundWindowQuota) error {
	persisted, err := s.GetInboundWindowQuotas(email)
	if err != nil {
		return err
	}
	for id := range quotas {
		q, ok := persisted[id]
		if !ok {
			return fmt.Errorf("inbound %d is not attached to client", id)
		}
		ib, err := inboundSvc.GetInbound(id)
		if err != nil {
			return err
		}
		if ib.NodeID == nil {
			continue
		}
		rt, err := inboundSvc.runtimeFor(ib)
		if err != nil {
			return err
		}
		remote, ok := rt.(*runtime.Remote)
		if !ok {
			return fmt.Errorf("inbound %d is not managed by a remote runtime", id)
		}
		ctx, cancel := nodePushContext()
		err = remote.SetClientWindowQuota(ctx, ib, email, q.QuotaBytes, q.Hours, q.Mode,
			*q.WindowExhaustAction, *q.WindowExhaustUpKbps, *q.WindowExhaustDownKbps, *q.WindowOverageMultiplierBps)
		cancel()
		if err != nil {
			if result := nodePolicyPushFailure(ib, fmt.Sprintf("入站 %d 的窗口配额", id), fmt.Errorf("push inbound %d window quota: %w", id, err), defaultWindowPolicy(q)); result != nil {
				return result
			}
		}
	}
	return nil
}

func (s *ClientService) GetInboundWindowQuotas(email string) (map[int]InboundWindowQuota, error) {
	var rows []model.ClientInbound
	err := database.GetDB().Table("client_inbounds").
		Select("client_inbounds.inbound_id, client_inbounds.window_quota_bytes, client_inbounds.window_hours, client_inbounds.window_mode, client_inbounds.window_exhaust_action, client_inbounds.window_exhaust_up_kbps, client_inbounds.window_exhaust_down_kbps, client_inbounds.window_overage_multiplier_bps").
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("clients.email = ?", email).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	quotas := make(map[int]InboundWindowQuota, len(rows))
	for _, row := range rows {
		action, up, down, multiplier := row.WindowExhaustAction, row.WindowExhaustUpKbps, row.WindowExhaustDownKbps, row.WindowOverageMultiplierBps
		if action == "" {
			action = "stop"
		}
		if multiplier == 0 {
			multiplier = 10000
		}
		quotas[row.InboundId] = InboundWindowQuota{
			QuotaBytes: row.WindowQuotaBytes, Hours: row.WindowHours, Mode: row.WindowMode,
			WindowExhaustAction: &action, WindowExhaustUpKbps: &up, WindowExhaustDownKbps: &down,
			WindowOverageMultiplierBps: &multiplier,
		}
	}
	return quotas, nil
}

func (s *ClientService) SetInboundWindowQuotas(email string, quotas map[int]InboundWindowQuota) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var client model.ClientRecord
		if err := tx.Where("email = ?", email).First(&client).Error; err != nil {
			return err
		}
		var links []model.ClientInbound
		if err := tx.Where("client_id = ?", client.Id).Find(&links).Error; err != nil {
			return err
		}
		attached := make(map[int]model.ClientInbound, len(links))
		for _, link := range links {
			attached[link.InboundId] = link
		}
		for id, q := range quotas {
			link, ok := attached[id]
			if !ok {
				return fmt.Errorf("inbound %d is not attached to client", id)
			}
			if q.QuotaBytes < 0 || q.Hours < 0 || q.Hours > 8760 || (q.QuotaBytes > 0 && q.Hours == 0) || (q.Mode != "" && q.Mode != "fixed" && q.Mode != "rolling") {
				return fmt.Errorf("inbound %d has an invalid window quota", id)
			}
			action, up, down, multiplier := link.WindowExhaustAction, link.WindowExhaustUpKbps, link.WindowExhaustDownKbps, link.WindowOverageMultiplierBps
			if q.WindowExhaustAction != nil {
				action = *q.WindowExhaustAction
			}
			if q.WindowExhaustUpKbps != nil {
				up = *q.WindowExhaustUpKbps
			}
			if q.WindowExhaustDownKbps != nil {
				down = *q.WindowExhaustDownKbps
			}
			if q.WindowOverageMultiplierBps != nil {
				multiplier = *q.WindowOverageMultiplierBps
			}
			if action == "" {
				action = "stop"
			}
			if multiplier == 0 {
				multiplier = 10000
			}
			if (action != "stop" && action != "throttle") || up < 0 || up > maxSpeedLimitKbps || down < 0 || down > maxSpeedLimitKbps || (action == "throttle" && (up == 0 || down == 0)) || multiplier < 10000 || multiplier > 1000000 {
				return fmt.Errorf("inbound %d has an invalid window exhaust policy", id)
			}
		}
		for id, q := range quotas {
			mode := q.Mode
			if mode == "" {
				mode = "fixed"
			}
			link := attached[id]
			action, up, down, multiplier := link.WindowExhaustAction, link.WindowExhaustUpKbps, link.WindowExhaustDownKbps, link.WindowOverageMultiplierBps
			if q.WindowExhaustAction != nil {
				action = *q.WindowExhaustAction
			}
			if q.WindowExhaustUpKbps != nil {
				up = *q.WindowExhaustUpKbps
			}
			if q.WindowExhaustDownKbps != nil {
				down = *q.WindowExhaustDownKbps
			}
			if q.WindowOverageMultiplierBps != nil {
				multiplier = *q.WindowOverageMultiplierBps
			}
			if action == "" {
				action = "stop"
			}
			if multiplier == 0 {
				multiplier = 10000
			}
			if err := tx.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", client.Id, id).
				Updates(map[string]any{"window_quota_bytes": q.QuotaBytes, "window_hours": q.Hours, "window_mode": mode, "window_exhaust_action": action, "window_exhaust_up_kbps": up, "window_exhaust_down_kbps": down, "window_overage_multiplier_bps": multiplier}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *ClientService) GetInboundRates(email string) (map[int]int64, error) {
	var rows []model.ClientInbound
	err := database.GetDB().Table("client_inbounds").
		Select("client_inbounds.inbound_id, client_inbounds.speed_limit_kbps").
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("clients.email = ?", email).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	rates := make(map[int]int64, len(rows))
	for _, row := range rows {
		rates[row.InboundId] = row.SpeedLimitKbps
	}
	return rates, nil
}

func (s *ClientService) SetInboundRates(email string, rates map[int]int64) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var client model.ClientRecord
		if err := tx.Where("email = ?", email).First(&client).Error; err != nil {
			return err
		}
		var links []model.ClientInbound
		if err := tx.Where("client_id = ?", client.Id).Find(&links).Error; err != nil {
			return err
		}
		attached := make(map[int]bool, len(links))
		for _, link := range links {
			attached[link.InboundId] = true
		}
		for id, kbps := range rates {
			if !attached[id] {
				return fmt.Errorf("inbound %d is not attached to client", id)
			}
			if kbps < 0 || kbps > 1000000000 {
				return fmt.Errorf("inbound %d speedLimitKbps must be between 0 and 1000000000", id)
			}
		}
		for id, kbps := range rates {
			if err := tx.Model(&model.ClientInbound{}).
				Where("client_id = ? AND inbound_id = ?", client.Id, id).
				Updates(map[string]any{"speed_limit_kbps": kbps, "speed_limit_up_kbps": nil, "speed_limit_down_kbps": nil}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
