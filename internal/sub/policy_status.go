package sub

import (
	"math"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// SubPolicyStatus describes the configured limits and the state currently
// visible to a subscription user. Bytes in ActualUsed are physical transfer;
// ExtraCharged is only the additional quota debit from overage multipliers.
type SubPolicyStatus struct {
	EffectiveState        string `json:"effectiveState"`
	TotalAction           string `json:"totalAction"`
	WindowAction          string `json:"windowAction"`
	TotalMultiplierBps    int    `json:"totalMultiplierBps"`
	WindowMultiplierBps   int    `json:"windowMultiplierBps"`
	TotalExhausted        bool   `json:"totalExhausted"`
	WindowExhausted       bool   `json:"windowExhausted"`
	GraceActive           bool   `json:"graceActive"`
	GraceEndsAt           int64  `json:"graceEndsAt"`
	GraceRemainingBytes   int64  `json:"graceRemainingBytes"`
	GraceHours            int    `json:"graceHours"`
	GraceQuotaBytes       int64  `json:"graceQuotaBytes"`
	ActualUsedBytes       int64  `json:"actualUsedBytes"`
	ExtraChargedBytes     int64  `json:"extraChargedBytes"`
	ChargedUsedBytes      int64  `json:"chargedUsedBytes"`
	TotalExhaustUpKbps    int64  `json:"totalExhaustUpKbps"`
	TotalExhaustDownKbps  int64  `json:"totalExhaustDownKbps"`
	WindowExhaustUpKbps   int64  `json:"windowExhaustUpKbps"`
	WindowExhaustDownKbps int64  `json:"windowExhaustDownKbps"`
	EffectiveUpKbps       int64  `json:"effectiveUpKbps"`
	EffectiveDownKbps     int64  `json:"effectiveDownKbps"`
}

func subPolicyAction(action string) string {
	if action == "throttle" {
		return "throttle"
	}
	return "stop"
}

func positiveSum(a, b int64) int64 {
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

func minPositive(current, next int64) int64 {
	if next <= 0 {
		return current
	}
	if current <= 0 || next < current {
		return next
	}
	return current
}

// Subscription headers and status remarks only need the account-wide window.
// Querying node windows here would make every subscription fetch wait for
// remote panels, even when a node is offline.
func (s *SubService) loadGlobalWindowQuota(subID string) *service.WindowStatus {
	db := database.GetDB()
	if db == nil || subID == "" {
		return nil
	}
	var client model.ClientRecord
	if db.Select("id", "window_quota_bytes", "window_hours", "window_mode").Where("sub_id = ?", subID).First(&client).Error != nil ||
		client.WindowQuotaBytes <= 0 || client.WindowHours <= 0 {
		return nil
	}
	status, err := service.WindowQuotaStatus(db, client.Id, 0, client.WindowQuotaBytes, client.WindowHours, client.WindowMode, time.Now())
	if err != nil {
		return nil
	}
	return &status
}

func (s *SubService) loadSubPolicyStatus(subID string, aggregate xray.ClientTraffic, window *service.WindowStatus) *SubPolicyStatus {
	db := database.GetDB()
	if db == nil || subID == "" {
		return nil
	}
	var client model.ClientRecord
	if db.Where("sub_id = ?", subID).First(&client).Error != nil {
		return nil
	}
	return clientPolicyStatus(client, aggregate, window)
}

// clientPolicyStatus evaluates one account against its own traffic and limits.
// A subscription ID may be shared by several accounts, so the aggregate
// traffic must never be compared with the first account's allowance.
func clientPolicyStatus(client model.ClientRecord, aggregate xray.ClientTraffic, window *service.WindowStatus) *SubPolicyStatus {
	actual := positiveSum(aggregate.Up, aggregate.Down)
	extra := max(aggregate.ChargeExtraBytes, 0)
	charged := positiveSum(actual, extra)
	charged = max(0, charged-max(aggregate.ChargeDiscountBytes, 0))
	status := &SubPolicyStatus{
		EffectiveState:        "active",
		TotalAction:           subPolicyAction(client.TotalExhaustAction),
		WindowAction:          subPolicyAction(client.WindowExhaustAction),
		TotalMultiplierBps:    model.EffectiveOverageMultiplierBps(client.TotalOverageMultiplierBps),
		WindowMultiplierBps:   model.EffectiveOverageMultiplierBps(client.WindowOverageMultiplierBps),
		TotalExhausted:        client.TotalGB > 0 && charged >= client.TotalGB,
		WindowExhausted:       window != nil && window.QuotaBytes > 0 && window.RemainingBytes <= 0,
		GraceHours:            client.GraceHours,
		GraceQuotaBytes:       client.GraceQuotaBytes,
		ActualUsedBytes:       actual,
		ExtraChargedBytes:     extra,
		ChargedUsedBytes:      charged,
		TotalExhaustUpKbps:    client.TotalExhaustUpKbps,
		TotalExhaustDownKbps:  client.TotalExhaustDownKbps,
		WindowExhaustUpKbps:   client.WindowExhaustUpKbps,
		WindowExhaustDownKbps: client.WindowExhaustDownKbps,
	}
	if client.ExpiryTime > 0 && client.GraceHours > 0 {
		status.GraceEndsAt = client.ExpiryTime + int64(client.GraceHours)*int64(time.Hour/time.Millisecond)
		var row xray.ClientTraffic
		baseline := actual
		if database.GetDB().Where("email = ?", client.Email).First(&row).Error == nil && row.GraceBaselineExpiry == client.ExpiryTime {
			baseline = positiveSum(row.GraceBaselineBytes, 0)
		}
		if client.GraceQuotaBytes == 0 {
			status.GraceRemainingBytes = math.MaxInt64
		} else {
			status.GraceRemainingBytes = max(0, client.GraceQuotaBytes-max(0, actual-baseline))
		}
	}
	now := time.Now().UnixMilli()
	expired := client.ExpiryTime > 0 && now >= client.ExpiryTime
	status.GraceActive = expired && now < status.GraceEndsAt && status.GraceRemainingBytes > 0 && client.GraceUpKbps > 0 && client.GraceDownKbps > 0
	totalStop := status.TotalExhausted && status.TotalAction == "stop"
	windowStop := status.WindowExhausted && status.WindowAction == "stop"
	if !client.Enable || totalStop || windowStop || (expired && !status.GraceActive) {
		status.EffectiveState = "blocked"
		status.GraceActive = false
		return status
	}
	if status.TotalExhausted && status.TotalAction == "throttle" {
		status.EffectiveUpKbps = minPositive(status.EffectiveUpKbps, client.TotalExhaustUpKbps)
		status.EffectiveDownKbps = minPositive(status.EffectiveDownKbps, client.TotalExhaustDownKbps)
		status.EffectiveState = "throttled"
	}
	if status.WindowExhausted && status.WindowAction == "throttle" {
		status.EffectiveUpKbps = minPositive(status.EffectiveUpKbps, client.WindowExhaustUpKbps)
		status.EffectiveDownKbps = minPositive(status.EffectiveDownKbps, client.WindowExhaustDownKbps)
		status.EffectiveState = "throttled"
	}
	if status.GraceActive {
		status.EffectiveUpKbps = minPositive(status.EffectiveUpKbps, client.GraceUpKbps)
		status.EffectiveDownKbps = minPositive(status.EffectiveDownKbps, client.GraceDownKbps)
		status.EffectiveState = "grace"
	}
	return status
}

func (s *SubService) subscriptionHeaderTraffic(subID string, traffic xray.ClientTraffic) xray.ClientTraffic {
	// A shared subscription has independent paid and grace deadlines. Never
	// publish the first account's grace deadline as the whole plan's deadline.
	var accountCount int64
	if db := database.GetDB(); db != nil {
		if db.Model(&model.ClientRecord{}).Where("sub_id = ?", subID).Count(&accountCount).Error == nil && accountCount > 1 {
			return traffic
		}
	}
	window := s.loadGlobalWindowQuota(subID)
	status := s.loadSubPolicyStatus(subID, traffic, window)
	if status != nil && status.GraceActive && status.EffectiveState == "grace" {
		traffic.ExpiryTime = status.GraceEndsAt
	}
	return traffic
}
