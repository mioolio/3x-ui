package model

// ClientLinkPolicy is the complete per-inbound policy copied from the master
// panel to a node during reconciliation. Sending default values as well lets a
// recovered node clear restrictions that were removed while it was offline.
type ClientLinkPolicy struct {
	Email                      string `json:"email"`
	SpeedLimitKbps             int64  `json:"speedLimitKbps"`
	SpeedLimitUpKbps           *int64 `json:"speedLimitUpKbps"`
	SpeedLimitDownKbps         *int64 `json:"speedLimitDownKbps"`
	WindowQuotaBytes           int64  `json:"windowQuotaBytes"`
	WindowHours                int    `json:"windowHours"`
	WindowMode                 string `json:"windowMode"`
	WindowExhaustAction        string `json:"windowExhaustAction"`
	WindowExhaustUpKbps        int64  `json:"windowExhaustUpKbps"`
	WindowExhaustDownKbps      int64  `json:"windowExhaustDownKbps"`
	WindowOverageMultiplierBps int    `json:"windowOverageMultiplierBps"`
}
