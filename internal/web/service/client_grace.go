package service

import (
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
)

// captureGraceBaselines runs before the poll's physical deltas are committed.
// The first poll after a deadline may include some pre-expiry traffic, but
// storing the baseline here prevents panel restarts and node reconciles from
// replenishing an allowance that has already been spent.
func captureGraceBaselines(tx *gorm.DB, now time.Time) error {
	return tx.Exec(fmt.Sprintf(`UPDATE client_traffics
		SET grace_baseline_bytes = CASE WHEN up > %d - down THEN %d ELSE up + down END,
		    grace_baseline_expiry = expiry_time
		WHERE expiry_time > 0 AND expiry_time <= ?
		  AND grace_baseline_expiry <> expiry_time
		  AND EXISTS (SELECT 1 FROM clients c WHERE c.email = client_traffics.email AND c.grace_hours > 0)`,
		math.MaxInt64, math.MaxInt64), now.UnixMilli()).Error
}
