package xray

// ClientTraffic represents traffic statistics and limits for a specific client.
// It tracks upload/download usage, expiry times, and online status for inbound clients.
type ClientTraffic struct {
	Id         int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement" example:"14825"`
	InboundId  int    `json:"inboundId" form:"inboundId" gorm:"index:idx_client_traffics_inbound" example:"1"`
	Enable     bool   `json:"enable" form:"enable" example:"true"`
	Email      string `json:"email" form:"email" gorm:"unique" example:"user1"`
	UUID       string `json:"uuid" form:"uuid" gorm:"-" example:"e18c9a96-71bf-48d4-933f-8b9a46d4290c"`
	SubId      string `json:"subId" form:"subId" gorm:"-" example:"i7tvdpeffi0hvvf1"`
	Up         int64  `json:"up" form:"up" example:"1048576"`
	Down       int64  `json:"down" form:"down" example:"2097152"`
	ExpiryTime int64  `json:"expiryTime" form:"expiryTime" gorm:"index:idx_client_traffics_renew,priority:1" example:"1735689600000"`
	Total      int64  `json:"total" form:"total" example:"10737418240"`
	Reset      int    `json:"reset" form:"reset" gorm:"default:0;index:idx_client_traffics_renew,priority:2" example:"0"`
	// ResetDay renews on that day of each calendar month instead of every
	// Reset days; 0 keeps the interval behaviour.
	ResetDay int `json:"resetDay" form:"resetDay" gorm:"default:0" example:"0"`
	// ResetMax caps how many times auto-renew may fire; 0 means no cap.
	ResetMax int `json:"resetMax" form:"resetMax" gorm:"default:0" example:"0"`
	// ResetCount is how many have fired, so a prepaid plan stops on its own.
	ResetCount   int   `json:"resetCount" form:"resetCount" gorm:"default:0" example:"0"`
	LastOnline   int64 `json:"lastOnline" form:"lastOnline" gorm:"default:0" example:"1735680000000"`
	LastSubFetch int64 `json:"lastSubFetch" form:"lastSubFetch" gorm:"default:0" example:"1735680000000"`
	// Sliding-window quota state (per-client windowQuotaGB/windowMinutes).
	// windowStarted is the current window's opening time in ms; windowDisabled
	// marks a client switched off BY the window action so the slide can restore
	// it without resurrecting an operator-disabled client.
	WindowUsed     int64 `json:"windowUsed" form:"windowUsed" gorm:"column:window_used;default:0" example:"1048576"`
	WindowStarted  int64 `json:"windowStarted" form:"windowStarted" gorm:"column:window_started;default:0" example:"1735680000000"`
	WindowDisabled bool  `json:"windowDisabled" form:"windowDisabled" gorm:"column:window_disabled;default:false" example:"false"`
	// Lifetime counters: never reset by quota renewals or operator resets, so
	// the panel can show total historical usage per client. Cleared only when
	// the accounting row itself is deleted.
	HistoryUp   int64 `json:"historyUp" form:"historyUp" gorm:"column:history_up;default:0" example:"10485760"`
	HistoryDown int64 `json:"historyDown" form:"historyDown" gorm:"column:history_down;default:0" example:"20971520"`
	// When depletion throttling started (ms); 0 = not currently throttled.
	ThrottledSince int64 `json:"throttledSince" form:"throttledSince" gorm:"column:throttled_since;default:0" example:"1735680000000"`
	// Period-plan state (client DepletionPeriod or inbound PlanPeriod, ms
	// fences aligned to first use). PeriodDisabled marks a client switched off
	// BY the period action so the fence roll restores it.
	PeriodUsed     int64 `json:"-" gorm:"column:period_used;default:0"`
	PeriodStarted  int64 `json:"-" gorm:"column:period_started;default:0"`
	PeriodDisabled bool  `json:"-" gorm:"column:period_disabled;default:false"`
}
