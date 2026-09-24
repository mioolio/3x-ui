package database

import (
	"fmt"
	"math"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"gorm.io/gorm"
)

type legacyDirectionalCharge struct {
	Id                      int64
	Up, Down                int64
	ChargeExtraBytes        int64
	ChargeDiscountBytes     int64
	ChargeExtraUpBytes      int64
	ChargeExtraDownBytes    int64
	ChargeDiscountUpBytes   int64
	ChargeDiscountDownBytes int64
}

func remainingLegacyCharge(total, up, down int64) int64 {
	total, up, down = max(0, total), max(0, up), max(0, down)
	if total <= up {
		return 0
	}
	total -= up
	if total <= down {
		return 0
	}
	return total - down
}

func addLegacyCharge(current, allocation int64) int64 {
	current, allocation = max(0, current), max(0, allocation)
	if current > math.MaxInt64-allocation {
		return math.MaxInt64
	}
	return current + allocation
}

func reconcileDirectionalCharge(total, directionalUp, directionalDown, physicalUp, physicalDown int64) (int64, int64) {
	total, directionalUp, directionalDown = max(0, total), max(0, directionalUp), max(0, directionalDown)
	if directionalUp > total || directionalDown > total-directionalUp {
		// Malformed old rows keep their aggregate debit and approximate split.
		return xray.AllocateLegacyCharge(total, directionalUp, directionalDown)
	}
	additionalUp, additionalDown := xray.AllocateLegacyCharge(
		remainingLegacyCharge(total, directionalUp, directionalDown), physicalUp, physicalDown)
	return addLegacyCharge(directionalUp, additionalUp), addLegacyCharge(directionalDown, additionalDown)
}

func directionalMismatchSQL(totalColumn, upColumn, downColumn string) string {
	total := "COALESCE(" + totalColumn + ", 0)"
	up := "COALESCE(" + upColumn + ", 0)"
	down := "COALESCE(" + downColumn + ", 0)"
	// CASE avoids evaluating up+down when the sum could overflow BIGINT on
	// PostgreSQL. The startup repair already clamps all columns nonnegative.
	return fmt.Sprintf("(CASE WHEN %s > %s OR %s > %s - %s THEN 1 WHEN %s + %s <> %s THEN 1 ELSE 0 END = 1)",
		up, total, down, total, up, up, down, total)
}

// backfillDirectionalCharges runs after AutoMigrate adds the four new columns.
// Existing rows retain their exact aggregate debit: only its unknowable
// upload/download split is estimated from physical transfer. It is safe to
// rerun after an interrupted upgrade because balanced rows are left untouched.
// Node baselines and global overlays get the same rule.
func backfillDirectionalCharges() error {
	for _, table := range []string{"client_traffics", "node_client_traffics", "client_global_traffics"} {
		var lastID int64
		for {
			var rows []legacyDirectionalCharge
			err := db.Table(table).
				Where("id > ? AND ("+
					directionalMismatchSQL("charge_extra_bytes", "charge_extra_up_bytes", "charge_extra_down_bytes")+" OR "+
					directionalMismatchSQL("charge_discount_bytes", "charge_discount_up_bytes", "charge_discount_down_bytes")+")", lastID).
				Order("id").Limit(400).Find(&rows).Error
			if err != nil {
				return fmt.Errorf("load %s directional charges: %w", table, err)
			}
			if len(rows) == 0 {
				break
			}
			if err := db.Transaction(func(tx *gorm.DB) error {
				for _, row := range rows {
					extraUp, extraDown := reconcileDirectionalCharge(row.ChargeExtraBytes, row.ChargeExtraUpBytes, row.ChargeExtraDownBytes, row.Up, row.Down)
					discountUp, discountDown := reconcileDirectionalCharge(row.ChargeDiscountBytes, row.ChargeDiscountUpBytes, row.ChargeDiscountDownBytes, row.Up, row.Down)
					if err := tx.Table(table).Where("id = ?", row.Id).Updates(map[string]any{
						"charge_extra_up_bytes":      extraUp,
						"charge_extra_down_bytes":    extraDown,
						"charge_discount_up_bytes":   discountUp,
						"charge_discount_down_bytes": discountDown,
					}).Error; err != nil {
						return fmt.Errorf("backfill %s row %d: %w", table, row.Id, err)
					}
				}
				return nil
			}); err != nil {
				return err
			}
			lastID = rows[len(rows)-1].Id
		}
	}
	return nil
}
