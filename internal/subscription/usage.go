package subscription

// Usage is the subscription's traffic and expiry (hyperUsage).
type Usage struct {
	Upload   uint64  `json:"upload"`
	Download uint64  `json:"download"`
	Total    *uint64 `json:"total,omitempty"`
	Expires  string  `json:"expireAt,omitempty"`
	NoExpiry bool    `json:"no_expiry,omitempty"`
}
