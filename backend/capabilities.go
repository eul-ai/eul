package backend

import (
	"context"
	"time"

	"github.com/eul-ai/eul/agent"
)

type CredentialChecker interface {
	CheckCredentials(context.Context) error
}

type LoginMethod string

const (
	LoginBrowser LoginMethod = "browser"
	LoginDevice  LoginMethod = "device"
)

type DeviceCode struct {
	VerificationURL string
	UserCode        string
}

type LoginInteraction struct {
	AuthURL    func(string) error
	DeviceCode func(DeviceCode) error
}

type Authenticator interface {
	Login(context.Context, LoginMethod, LoginInteraction) error
	Logout(context.Context) error
}

type AccountUsage struct {
	Windows           []UsageWindow
	MonthlyUsageUSD   *float64
	LimitRemainingUSD *float64
}

type UsageWindow struct {
	Duration    time.Duration
	UsedPercent int
	ResetsAt    time.Time
}

type UsageProvider interface {
	Usage(context.Context) (AccountUsage, error)
}

type ModelMetadata struct {
	ContextWindow  int64
	ThinkingLevels []agent.ThinkingLevel
	FastMode       bool
}

func (metadata ModelMetadata) ClampThinkingLevel(level agent.ThinkingLevel) agent.ThinkingLevel {
	return agent.ClampThinkingLevel(level, metadata.ThinkingLevels)
}

type ModelMetadataProvider interface {
	ModelMetadata(model string) ModelMetadata
}
