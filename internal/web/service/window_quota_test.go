package service

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWindowQuotaStatus(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:window-quota-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ClientWindowSample{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)
	fixedStart, fixedEnd := windowBounds(2, "fixed", now)
	if fixedStart != time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC).UnixMilli() || fixedEnd != time.Date(2026, 9, 24, 14, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("wrong fixed window: %d - %d", fixedStart, fixedEnd)
	}
	rows := []model.ClientWindowSample{
		{ClientId: 1, InboundId: 0, BucketStart: now.Add(-3 * time.Hour).UnixMilli(), Bytes: 90},
		{ClientId: 1, InboundId: 0, BucketStart: now.Add(-1 * time.Hour).UnixMilli(), Bytes: 40},
		{ClientId: 1, InboundId: 0, BucketStart: now.UnixMilli(), Bytes: 30},
		{ClientId: 1, InboundId: 7, BucketStart: now.UnixMilli(), Bytes: 80},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mode         string
		inboundID          int
		wantUsed, wantLeft int64
	}{
		{"fixed", "fixed", 0, 30, 70},
		{"rolling", "rolling", 0, 70, 30},
		{"per inbound", "fixed", 7, 80, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := WindowQuotaStatus(db, 1, tc.inboundID, 100, 2, tc.mode, now)
			if err != nil {
				t.Fatal(err)
			}
			if status.UsedBytes != tc.wantUsed || status.RemainingBytes != tc.wantLeft {
				t.Fatalf("got used=%d remaining=%d", status.UsedBytes, status.RemainingBytes)
			}
		})
	}
}
