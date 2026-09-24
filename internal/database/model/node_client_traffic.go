package model

type NodeClientTraffic struct {
	Id                      int    `json:"id" gorm:"primaryKey;autoIncrement"`
	NodeId                  int    `json:"nodeId" gorm:"uniqueIndex:idx_node_email,priority:1;not null"`
	Email                   string `json:"email" gorm:"uniqueIndex:idx_node_email,priority:2;not null"`
	Up                      int64  `json:"up"`
	Down                    int64  `json:"down"`
	ChargeExtraBytes        int64  `json:"chargeExtraBytes" gorm:"column:charge_extra_bytes;default:0"`
	ChargeDiscountBytes     int64  `json:"chargeDiscountBytes" gorm:"column:charge_discount_bytes;default:0"`
	ChargeExtraUpBytes      int64  `json:"chargeExtraUpBytes" gorm:"column:charge_extra_up_bytes;default:0"`
	ChargeExtraDownBytes    int64  `json:"chargeExtraDownBytes" gorm:"column:charge_extra_down_bytes;default:0"`
	ChargeDiscountUpBytes   int64  `json:"chargeDiscountUpBytes" gorm:"column:charge_discount_up_bytes;default:0"`
	ChargeDiscountDownBytes int64  `json:"chargeDiscountDownBytes" gorm:"column:charge_discount_down_bytes;default:0"`
}
