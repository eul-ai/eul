package backend

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/eul-ai/eul/agent"
)

type Backend struct {
	ID     string
	Name   string
	Models ModelDefaults
	// Open checks credentials and loads the model catalog. home is the resolved
	// absolute Eul application directory.
	Open func(ctx context.Context, home string) (*Runtime, error)
	// Auth is nil when the backend has no login.
	Auth func(home string) Authenticator
}

// Runtime is an opened backend. Provider is safe for concurrent use and is
// shared by every agent that uses the backend.
type Runtime struct {
	Provider agent.Provider
	Usage    func(context.Context) (AccountUsage, error)
	Catalog  map[string]ModelMetadata
}

func (runtime *Runtime) Model(name string) (ModelMetadata, error) {
	metadata, ok := runtime.Catalog[name]
	if !ok {
		return ModelMetadata{}, &ModelNotSupportedError{Model: name, AvailableModels: slices.Sorted(maps.Keys(runtime.Catalog))}
	}
	return metadata, nil
}

type Authenticator interface {
	LoginBrowser(context.Context, func(string) error) error
	LoginDevice(context.Context, func(DeviceCode) error) error
	Logout(context.Context) error
}

type DeviceCode struct {
	VerificationURL string
	UserCode        string
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

// Registry lists the available backends. Its first backend is the default.
type Registry []Backend

func (registry Registry) Lookup(id string) (Backend, error) {
	if id == "" {
		return registry[0], nil
	}
	for _, candidate := range registry {
		if candidate.ID == id {
			return candidate, nil
		}
	}
	return Backend{}, fmt.Errorf("backend: backend %q is not available", id)
}

func ValidID(id string) bool {
	for index, character := range id {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			continue
		}
		if index > 0 && strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return id != ""
}
