package terminal

import "time"

type ProviderUsage struct {
	Windows []UsageWindow
}

type UsageWindow struct {
	Duration    time.Duration
	UsedPercent int
	ResetsAt    time.Time
}
