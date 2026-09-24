package service

import (
	"context"
	"fmt"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/mtproto"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

// mtprotoWindowHardStops evaluates the window policies that mtg can enforce by
// removing a secret. The sidecar cannot apply a per-secret slow speed, so old
// or API-created throttle rules fail closed as a hard stop rather than silently
// allowing unlimited traffic.
func mtprotoWindowHardStops(db *gorm.DB, emails []string, inbounds []*model.Inbound) (map[string]bool, map[string]bool, error) {
	global := map[string]bool{}
	perInbound := map[string]bool{}
	if len(emails) == 0 || len(inbounds) == 0 {
		return global, perInbound, nil
	}
	now := time.Now()
	clientsByID := make(map[int]model.ClientRecord, len(emails))
	for _, batch := range chunkStrings(emails, 400) {
		var clients []model.ClientRecord
		if err := db.Where("email IN ?", batch).Find(&clients).Error; err != nil {
			return nil, nil, err
		}
		for _, client := range clients {
			clientsByID[client.Id] = client
			if client.WindowQuotaBytes <= 0 || client.WindowHours <= 0 {
				continue
			}
			status, err := WindowQuotaStatus(db, client.Id, 0, client.WindowQuotaBytes, client.WindowHours, client.WindowMode, now)
			if err != nil {
				return nil, nil, err
			}
			if status.RemainingBytes <= 0 {
				global[client.Email] = true
			}
		}
	}
	ids := make([]int, 0, len(inbounds))
	tags := make(map[int]string, len(inbounds))
	for _, inbound := range inbounds {
		ids = append(ids, inbound.Id)
		tags[inbound.Id] = inbound.Tag
	}
	var links []model.ClientInbound
	if err := db.Where("inbound_id IN ? AND window_quota_bytes > 0 AND window_hours > 0", ids).Find(&links).Error; err != nil {
		return nil, nil, err
	}
	for _, link := range links {
		client, found := clientsByID[link.ClientId]
		if !found {
			continue
		}
		status, err := WindowQuotaStatus(db, link.ClientId, link.InboundId, link.WindowQuotaBytes, link.WindowHours, link.WindowMode, now)
		if err != nil {
			return nil, nil, err
		}
		if status.RemainingBytes <= 0 {
			perInbound[tags[link.InboundId]+"\x00"+client.Email] = true
		}
	}
	return global, perInbound, nil
}

// ensureMtprotoRateBridges upgrades stored MTProto inbounds that already have a
// speed ceiling but predate the authenticated Xray bridge. Run this before
// building the core config so the sidecar and bridge use one persisted port and
// credential from their first start after an upgrade. A concurrent edit is not
// overwritten: the caller retries its config build with the latest row.
func (s *InboundService) ensureMtprotoRateBridges(inbounds []*model.Inbound) error {
	db := database.GetDB()
	for _, ib := range inbounds {
		if ib == nil || !ib.Enable || ib.NodeID != nil || !mtproto.InboundHasRate(ib) {
			continue
		}
		previous := ib.Settings
		if err := s.normalizeMtprotoXrayPort(ib, previous); err != nil {
			return fmt.Errorf("mtproto inbound %d: prepare rate bridge: %w", ib.Id, err)
		}
		if ib.Settings == previous {
			continue
		}
		result := db.Model(&model.Inbound{}).
			Where("id = ? AND settings = ?", ib.Id, previous).
			Update("settings", ib.Settings)
		if result.Error != nil {
			return fmt.Errorf("mtproto inbound %d: persist rate bridge: %w", ib.Id, result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("mtproto inbound %d changed during rate bridge upgrade; retry config build", ib.Id)
		}
	}
	return nil
}

// DesiredMtprotoInstances derives the mtg sidecar configs this panel should be
// running: one instance per enabled local mtproto inbound, serving only the
// secrets of clients that are both enabled in the inbound settings and not
// depletion-disabled in client_traffics. That is the same effective client set
// buildInboundForLocalRuntime pushes on interactive edits, so the reconcile job
// and the push paths agree on one fingerprint — a disagreement would surface
// as a needless mtg restart, and a job that read only the raw settings would
// keep serving depleted clients until an unrelated restart. Inbounds whose
// every secret is filtered away are omitted so Reconcile stops their sidecar.
func (s *InboundService) DesiredMtprotoInstances() ([]mtproto.Instance, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).
		Where("protocol = ? AND enable = ? AND node_id IS NULL", model.MTProto, true).
		Find(&inbounds).Error
	if err != nil {
		return nil, err
	}
	if len(inbounds) == 0 {
		return nil, nil
	}

	candidates := make([]mtproto.Instance, 0, len(inbounds))
	emailSet := make(map[string]struct{})
	for _, ib := range inbounds {
		inst, ok := mtproto.InstanceFromInbound(ib)
		if !ok {
			continue
		}
		candidates = append(candidates, inst)
		for _, sec := range inst.Secrets {
			emailSet[sec.Name] = struct{}{}
		}
	}
	emails := make([]string, 0, len(emailSet))
	for email := range emailSet {
		emails = append(emails, email)
	}
	var disabledRows []xray.ClientTraffic
	for _, batch := range chunkStrings(emails, 400) {
		var rows []xray.ClientTraffic
		if err := db.Model(xray.ClientTraffic{}).
			Where("email IN ? AND enable = ?", batch, false).
			Select("email").Find(&rows).Error; err != nil {
			return nil, err
		}
		disabledRows = append(disabledRows, rows...)
	}
	disabled := make(map[string]bool, len(disabledRows))
	for _, row := range disabledRows {
		disabled[row.Email] = true
	}
	globalWindowStops, inboundWindowStops, err := mtprotoWindowHardStops(db, emails, inbounds)
	if err != nil {
		return nil, err
	}

	instances := make([]mtproto.Instance, 0, len(candidates))
	for _, candidate := range candidates {
		inst := candidate
		kept := make([]mtproto.SecretEntry, 0, len(inst.Secrets))
		for _, sec := range inst.Secrets {
			if disabled[sec.Name] || globalWindowStops[sec.Name] || inboundWindowStops[inst.Tag+"\x00"+sec.Name] {
				continue
			}
			kept = append(kept, sec)
		}
		inst.Secrets = kept
		if len(inst.Secrets) == 0 {
			continue
		}
		instances = append(instances, inst)
	}
	return instances, nil
}

// applyLocalMtproto pushes a single local mtproto inbound's current client set
// to its mtg sidecar right after a client edit commits, so an add, removal,
// re-key or enable-toggle takes effect immediately instead of waiting up to
// 10s for the reconcile job. With a reload-capable mtg the change is applied in
// place without dropping other clients; older binaries fall back to a restart
// inside the manager. It re-reads the inbound so it sees the committed settings,
// filters depleted clients exactly like the reconcile job, and is a no-op for
// node-owned or non-mtproto inbounds. Failures are logged and swallowed: the
// reconcile job is the backstop, and an xray restart cannot help the sidecar.
func (s *InboundService) applyLocalMtproto(inboundId int) {
	inbound, err := s.GetInbound(inboundId)
	if err != nil || inbound == nil || inbound.Protocol != model.MTProto || inbound.NodeID != nil {
		return
	}
	rt, err := s.runtimeFor(inbound)
	if err != nil {
		return
	}
	payload := inbound
	if inbound.Enable {
		if built, bErr := s.buildInboundForLocalRuntime(database.GetDB(), inbound); bErr == nil {
			payload = built
		}
	}
	if err := rt.UpdateInbound(context.Background(), inbound, payload); err != nil {
		logger.Debug("mtproto: immediate client apply failed for inbound", inboundId, ":", err)
	}
}

func (s *InboundService) resetMtprotoClientQuota(email string) {
	mgr := mtproto.GetManager()
	if !mgr.HasRunning() {
		return
	}
	id, ok := s.localMtprotoInboundIdForEmail(email)
	if !ok {
		return
	}
	s.applyLocalMtproto(id)
	mgr.ResetQuota(email)
}

func (s *InboundService) resetAllMtprotoQuotas() {
	mgr := mtproto.GetManager()
	if !mgr.HasRunning() {
		return
	}
	desired, err := s.DesiredMtprotoInstances()
	if err != nil {
		return
	}
	mgr.Reconcile(desired)
	for _, inst := range desired {
		for _, sec := range inst.Secrets {
			mgr.ResetQuota(sec.Name)
		}
	}
}

func (s *InboundService) localMtprotoInboundIdForEmail(email string) (int, bool) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	if err := db.Model(model.Inbound{}).
		Where("protocol = ? AND node_id IS NULL", model.MTProto).
		Find(&inbounds).Error; err != nil {
		return 0, false
	}
	for _, ib := range inbounds {
		inst, ok := mtproto.InstanceFromInbound(ib)
		if !ok {
			continue
		}
		for _, sec := range inst.Secrets {
			if sec.Name == email {
				return ib.Id, true
			}
		}
	}
	return 0, false
}
