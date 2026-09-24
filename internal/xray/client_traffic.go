package xray

// InboundClientTraffic is the traffic delta for one authenticated client on
// one inbound, emitted by the bundled Xray dispatcher.
type InboundClientTraffic struct {
	Tag   string
	Email string
	Up    int64
	Down  int64
}

// ClientTraffic represents traffic statistics and limits for a specific client.
// It tracks upload/download usage, expiry times, and online status for inbound clients.
type ClientTraffic struct {
	Id        int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement" example:"14825"`
	InboundId int    `json:"inboundId" form:"inboundId" gorm:"index:idx_client_traffics_inbound" example:"1"`
	Enable    bool   `json:"enable" form:"enable" example:"true"`
	Email     string `json:"email" form:"email" gorm:"unique" example:"user1"`
	UUID      string `json:"uuid" form:"uuid" gorm:"-" example:"e18c9a96-71bf-48d4-933f-8b9a46d4290c"`
	SubId     string `json:"subId" form:"subId" gorm:"-" example:"i7tvdpeffi0hvvf1"`
	Up        int64  `json:"up" form:"up" example:"1048576"`
	Down      int64  `json:"down" form:"down" example:"2097152"`
	// ChargeExtraBytes is the additional quota debit from configured overage
	// multipliers. Up/Down remain the physical transfer counters everywhere.
	ChargeExtraBytes int64 `json:"chargeExtraBytes" gorm:"column:charge_extra_bytes;default:0"`
	// ChargeDiscountBytes is a nonnegative cumulative allowance credit from
	// an inbound whose traffic multiplier is below 1x.
	ChargeDiscountBytes int64 `json:"chargeDiscountBytes" gorm:"column:charge_discount_bytes;default:0"`
	// ChargeExtraDelta is populated only by the Xray stats poll. It is never
	// stored directly; AddTraffic accumulates it into ChargeExtraBytes.
	ChargeExtraDelta    int64 `json:"-" gorm:"-"`
	ChargeDiscountDelta int64 `json:"-" gorm:"-"`
	// A grace allowance starts at the paid expiry. The captured physical usage
	// is durable so a panel restart cannot replenish the grace allowance.
	GraceBaselineBytes  int64 `json:"-" gorm:"column:grace_baseline_bytes;default:0"`
	GraceBaselineExpiry int64 `json:"-" gorm:"column:grace_baseline_expiry;default:0"`
	QuotaEpoch          int64 `json:"-" gorm:"column:quota_epoch;default:0"`
	ExpiryTime          int64 `json:"expiryTime" form:"expiryTime" gorm:"index:idx_client_traffics_renew,priority:1" example:"1735689600000"`
	Total               int64 `json:"total" form:"total" example:"10737418240"`
	Reset               int   `json:"reset" form:"reset" gorm:"default:0;index:idx_client_traffics_renew,priority:2" example:"0"`
	// ResetDay renews on that day of each calendar month instead of every
	// Reset days; 0 keeps the interval behaviour.
	ResetDay int `json:"resetDay" form:"resetDay" gorm:"default:0" example:"0"`
	// ResetMax caps how many times auto-renew may fire; 0 means no cap.
	ResetMax int `json:"resetMax" form:"resetMax" gorm:"default:0" example:"0"`
	// ResetCount is how many have fired, so a prepaid plan stops on its own.
	ResetCount   int   `json:"resetCount" form:"resetCount" gorm:"default:0" example:"0"`
	LastOnline   int64 `json:"lastOnline" form:"lastOnline" gorm:"default:0" example:"1735680000000"`
	LastSubFetch int64 `json:"lastSubFetch" form:"lastSubFetch" gorm:"default:0" example:"1735680000000"`
}
