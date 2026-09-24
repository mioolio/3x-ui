package service

import (
	"fmt"
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
		Select("COALESCE(SUM(bytes), 0) AS used_bytes, COALESCE(MIN(bucket_start), 0) AS oldest_bucket").Scan(&usage).Error; err != nil {
		return status, err
	}
	status.UsedBytes = usage.UsedBytes
	if mode == "rolling" && usage.OldestBucket > 0 {
		status.ResetAt = usage.OldestBucket + int64(hours)*int64(time.Hour/time.Millisecond) + windowBucketMillis
	}
	status.RemainingBytes = max(0, quota-status.UsedBytes)
	return status, nil
}

var lastWindowCleanup atomic.Int64

// RecordWindowTraffic commits the same deltas as the main traffic poll into
// minute buckets. Global counters and per-inbound counters are independent;
// one connection contributes to each exactly once.
func (s *XrayService) RecordWindowTraffic(global []*xray.ClientTraffic, perInbound []*xray.InboundClientTraffic) error {
	db := database.GetDB()
	if db == nil {
		return nil
	}
	emailSet := map[string]bool{}
	tagSet := map[string]bool{}
	for _, row := range global {
		if row != nil && row.Up+row.Down > 0 {
			emailSet[row.Email] = true
		}
	}
	for _, row := range perInbound {
		if row != nil && row.Up+row.Down > 0 {
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
	tags := map[string]int{}
	for _, batch := range chunkStrings(tagList, 400) {
		var rows []model.Inbound
		if err := db.Select("id", "tag").Where("tag IN ? AND node_id IS NULL", batch).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			tags[row.Tag] = row.Id
		}
	}
	bucket := (time.Now().UnixMilli() / windowBucketMillis) * windowBucketMillis
	type key struct{ clientID, inboundID int }
	amounts := map[key]int64{}
	for _, row := range global {
		if row == nil {
			continue
		}
		if id := ids[row.Email]; id > 0 && row.Up+row.Down > 0 {
			amounts[key{id, 0}] += row.Up + row.Down
		}
	}
	for _, row := range perInbound {
		if row == nil {
			continue
		}
		if id, inboundID := ids[row.Email], tags[row.Tag]; id > 0 && inboundID > 0 && row.Up+row.Down > 0 {
			amounts[key{id, inboundID}] += row.Up + row.Down
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		for k, amount := range amounts {
			if err := tx.Exec(`INSERT INTO client_window_samples (client_id, inbound_id, bucket_start, bytes) VALUES (?, ?, ?, ?)
				ON CONFLICT (client_id, inbound_id, bucket_start) DO UPDATE SET bytes = client_window_samples.bytes + excluded.bytes`,
				k.clientID, k.inboundID, bucket, amount).Error; err != nil {
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
