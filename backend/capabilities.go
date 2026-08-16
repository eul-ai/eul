package backend

import (
	"context"
	"time"

	"github.com/eul-ai/eul/agent"
)

type CredentialChecker interface {
	CheckCredentials(context.Context) error
}

type DeviceCode struct {
	VerificationURL string
	UserCode        string
}

type BrowserAuthenticator interface {
	LoginBrowser(context.Context, func(string) error) error
}

type DeviceAuthenticator interface {
	LoginDevice(context.Context, func(DeviceCode) error) error
}

type Authenticator interface {
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

type ModelInitializer interface {
	InitializeModels(context.Context) error
}

type ModelValidator interface {
	ValidateModel(model string) error
}
