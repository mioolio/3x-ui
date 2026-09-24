package service

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

const windowBucketMillis int64 = 60_000

type WindowStatus struct {
	QuotaBytes     int64  `json:"quotaBytes"`
	UsedBytes      int64  `json:"usedBytes"`
	RemainingBytes int64  `json:"remainingBytes"`
	WindowHours    int    `json:"windowHours"`
	WindowMode     string `json:"windowMode"`
	WindowStart    int64  `json:"windowStart"`
	ResetAt        int64  `json:"resetAt"`
}

// windowBounds anchors fixed windows to UTC epoch. Rolling windows use the
// moving lower bound; samples are minute buckets, so the boundary is accurate
// to one minute while retaining compact storage under sustained traffic.
func windowBounds(hours int, mode string, now time.Time) (int64, int64) {
	duration := int64(hours) * int64(time.Hour/time.Millisecond)
	if duration <= 0 {
		return 0, 0
	}
	current := now.UnixMilli()
	if mode == "rolling" {
		return ((current - duration) / windowBucketMillis) * windowBucketMillis, 0
	}
	start := (current / duration) * duration
	return start, start + duration
}

func WindowQuotaStatus(db *gorm.DB, clientID, inboundID int, quota int64, hours int, mode string, now time.Time) (WindowStatus, error) {
	status := WindowStatus{QuotaBytes: quota, WindowHours: hours, WindowMode: mode}
	if quota <= 0 || hours <= 0 {
		return status, nil
	}
	status.WindowStart, status.ResetAt = windowBounds(hours, mode, now)
	var usage struct {
		UsedBytes    int64
		OldestBucket int64
	}
	if err := db.Model(&model.ClientWindowSample{}).
		Where("client_id = ? AND inbound_id = ? AND bucket_start >= ?", clientID, inboundID, status.WindowStart).
		Select("COALESCE(SUM(COALESCE(charged_bytes, bytes)), 0) AS used_bytes, COALESCE(MIN(bucket_start), 0) AS oldest_bucket").Scan(&usage).Error; err != nil {
		// An unusually large number of near-limit buckets can overflow SQLite's
		// integer SUM or the int64 scan of PostgreSQL's numeric SUM. Stream only
		// that exceptional case and saturate in Go.
		rows, rowErr := db.Model(&model.ClientWindowSample{}).
			Where("client_id = ? AND inbound_id = ? AND bucket_start >= ?", clientID, inboundID, status.WindowStart).
			Select("bucket_start, COALESCE(charged_bytes, bytes)").Order("bucket_start").Rows()
		if rowErr != nil {
			return status, rowErr
		}
		defer rows.Close()
		var total big.Int
		for rows.Next() {
			var bucket, amount int64
			if rowErr = rows.Scan(&bucket, &amount); rowErr != nil {
				return status, rowErr
			}
			if usage.OldestBucket == 0 {
				usage.OldestBucket = bucket
			}
			total.Add(&total, big.NewInt(amount))
		}
		if rowErr = rows.Err(); rowErr != nil {
			return status, rowErr
		}
		if total.Sign() < 0 {
			usage.UsedBytes = 0
		} else if !total.IsInt64() {
			usage.UsedBytes = math.MaxInt64
		} else {
			usage.UsedBytes = total.Int64()
		}
	}
	status.UsedBytes = max(0, usage.UsedBytes)
	if mode == "rolling" && usage.OldestBucket > 0 {
		status.ResetAt = usage.OldestBucket + int64(hours)*int64(time.Hour/time.Millisecond) + windowBucketMillis
	}
	status.RemainingBytes = max(0, quota-status.UsedBytes)
	return status, nil
}

var lastWindowCleanup atomic.Int64

// RecordWindowTraffic commits physical and billed deltas from the same poll
// into minute buckets. Global and per-inbound counters are separate quota
// dimensions; one connection contributes to each exactly once.
func (s *XrayService) RecordWindowTraffic(global []*xray.ClientTraffic, perInbound []*xray.InboundClientTraffic) error {
	db := database.GetDB()
	if db == nil {
		return nil
	}
	emailSet := map[string]bool{}
	tagSet := map[string]bool{}
	for _, row := range global {
		if row != nil && (windowPhysical(row.Up, row.Down) > 0 || row.ChargeExtraDelta > 0 || row.ChargeDiscountDelta > 0) {
			emailSet[row.Email] = true
		}
	}
	for _, row := range perInbound {
		if row != nil && (windowPhysical(row.Up, row.Down) > 0 || row.ChargeExtraDelta > 0 || row.ChargeDiscountDelta > 0) {
			emailSet[row.Email] = true
			tagSet[row.Tag] = true
		}
	}
	if len(emailSet) == 0 {
		return nil
	}
	emailList := make([]string, 0, len(emailSet))
	for email := range emailSet {
		emailList = append(emailList, email)
	}
	tagList := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tagList = append(tagList, tag)
	}
	ids := map[string]int{}
	for _, batch := range chunkStrings(emailList, 400) {
		var rows []model.ClientRecord
		if err := db.Select("id", "email").Where("email IN ?", batch).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			ids[row.Email] = row.Id
		}
	}
	type inboundInfo struct {
		id     int
		factor int
	}
	tags := map[string]inboundInfo{}
	for _, batch := range chunkStrings(tagList, 400) {
		var rows []model.Inbound
		if err := db.Select("id", "tag", "traffic_multiplier_bps").Where("tag IN ? AND node_id IS NULL", batch).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			tags[row.Tag] = inboundInfo{id: row.Id, factor: row.TrafficMultiplierBps}
		}
	}
	bucket := (time.Now().UnixMilli() / windowBucketMillis) * windowBucketMillis
	type key struct{ clientID, inboundID int }
	type amount struct{ physical, charged int64 }
	amounts := map[key]amount{}
	for _, row := range global {
		if row == nil {
			continue
		}
		if id := ids[row.Email]; id > 0 {
			physical := windowPhysical(row.Up, row.Down)
			charged := windowCharged(physical, row.ChargeExtraDelta, row.ChargeDiscountDelta)
			k := key{id, 0}
			prior := amounts[k]
			prior.physical = saturatingPositiveSum(prior.physical, physical)
			prior.charged = windowSignedSum(prior.charged, charged)
			amounts[k] = prior
		}
	}
	for _, row := range perInbound {
		if row == nil {
			continue
		}
		if id, inbound := ids[row.Email], tags[row.Tag]; id > 0 && inbound.id > 0 {
			physical := windowPhysical(row.Up, row.Down)
			charged := windowCharged(physical, row.ChargeExtraDelta, row.ChargeDiscountDelta)
			if !row.ChargeCountersSeen {
				// Older cores report only physical per-inbound counters. Their
				// current configured factor is the best available approximation;
				// precise historical billing requires the charge counters above.
				charged = windowScaleFallback(physical, inbound.factor)
			}
			k := key{id, inbound.id}
			prior := amounts[k]
			prior.physical = saturatingPositiveSum(prior.physical, physical)
			prior.charged = windowSignedSum(prior.charged, charged)
			amounts[k] = prior
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		for k, delta := range amounts {
			if delta.physical == 0 && delta.charged == 0 {
				continue
			}
			physical := min(delta.physical, database.TrafficMax)
			charged := max(-database.TrafficMax, min(delta.charged, database.TrafficMax))
			// On the first write to an old minute bucket, the old physical
			// amount becomes its billed baseline. This keeps usage continuous
			// across deployment even when a poll lands in the same minute.
			query := fmt.Sprintf(`INSERT INTO client_window_samples (client_id, inbound_id, bucket_start, bytes, charged_bytes) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (client_id, inbound_id, bucket_start) DO UPDATE SET bytes = %s,
				charged_bytes = %s`,
				database.ClampedAddExpr("client_window_samples.bytes"),
				windowClampedChargedAddExpr())
			if err := tx.Exec(query, k.clientID, k.inboundID, bucket, physical, charged, physical).Error; err != nil {
				return fmt.Errorf("write window sample: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if previous := lastWindowCleanup.Load(); bucket-previous >= int64(time.Hour/time.Millisecond) && lastWindowCleanup.CompareAndSwap(previous, bucket) {
		cutoff := bucket - int64(8761*time.Hour/time.Millisecond)
		return db.Where("bucket_start < ?", cutoff).Delete(&model.ClientWindowSample{}).Error
	}
	return nil
}

func windowPhysical(up, down int64) int64 {
	return saturatingPositiveSum(up, down)
}

func windowCharged(physical, extra, discount int64) int64 {
	charged := saturatingPositiveSum(physical, extra)
	if discount > 0 {
		charged -= discount
	}
	return charged
}

func windowSignedSum(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

func windowClampedChargedAddExpr() string {
	base := "COALESCE(client_window_samples.charged_bytes, client_window_samples.bytes)"
	if database.IsPostgres() {
		return fmt.Sprintf("GREATEST(-%d, LEAST(%d, (%s)::numeric + excluded.charged_bytes::numeric))::bigint", database.TrafficMax, database.TrafficMax, base)
	}
	// SQLite promotes an overflowing INTEGER addition to REAL. The outer
	// bounds still return an exact integer constant before the value is stored.
	return fmt.Sprintf("MAX(-%d, MIN(%d, %s + excluded.charged_bytes))", database.TrafficMax, database.TrafficMax, base)
}

// windowScaleFallback is used only when an older core lacks scoped charge
// counters. The exact counters carry fractional bytes and the policy active
// when traffic flowed; a current factor cannot reproduce either detail.
func windowScaleFallback(physical int64, factor int) int64 {
	if physical <= 0 {
		return 0
	}
	factor = model.EffectiveTrafficMultiplierBps(factor)
	if factor < 100 || factor > model.MaxTrafficMultiplierBps {
		factor = model.DefaultTrafficMultiplierBps
	}
	hi, lo := bits.Mul64(uint64(physical), uint64(factor))
	if hi >= 10_000 {
		return math.MaxInt64
	}
	result, _ := bits.Div64(hi, lo, 10_000)
	if result > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(result)
}
