package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eul-ai/eul/agent"
)

type Descriptor struct {
	ID   string
	Name string
}

type ModelDefaults struct {
	Main     string
	Fast     string
	Balanced string
}

type Options struct {
	Home string
}

type AuthOptions struct {
	Home   string
	Device bool
}

type Interaction struct {
	OpenURL    func(string) error
	DeviceCode func(verificationURL, userCode string) error
}

// Instance is a configured backend. NewProvider may be called concurrently and
// each returned provider remains valid until Close is called. Close is called
// after all providers created by the instance are no longer in use.
type Instance interface {
	NewProvider() (agent.Provider, error)
	Close() error
}

// CredentialChecker is implemented by backend instances that can verify their
// credentials before the first provider request.
type CredentialChecker interface {
	CheckCredentials(context.Context) error
}

// Authenticator is implemented by drivers that support explicit login and
// logout commands.
type Authenticator interface {
	Login(context.Context, AuthOptions, Interaction) error
	Logout(context.Context, AuthOptions) error
}

type Driver interface {
	Descriptor() Descriptor
	ModelDefaults() ModelDefaults
	Configure(Options) (Instance, error)
}

type Registry struct {
	defaultID string
	drivers   map[string]Driver
}

func NewRegistry(defaultID string, drivers ...Driver) (*Registry, error) {
	if !ValidID(defaultID) {
		return nil, errors.New("backend: default provider ID is invalid")
	}

	registry := &Registry{defaultID: defaultID, drivers: make(map[string]Driver, len(drivers))}
	for _, driver := range drivers {
		if driver == nil {
			return nil, errors.New("backend: registered provider is nil")
		}
		descriptor := driver.Descriptor()
		if !ValidID(descriptor.ID) {
			return nil, errors.New("backend: registered provider ID is invalid")
		}
		if strings.TrimSpace(descriptor.Name) == "" {
			return nil, fmt.Errorf("backend: provider %q has no display name", descriptor.ID)
		}
		if _, exists := registry.drivers[descriptor.ID]; exists {
			return nil, fmt.Errorf("backend: duplicate provider %q", descriptor.ID)
		}
		registry.drivers[descriptor.ID] = driver
	}
	if _, exists := registry.drivers[defaultID]; !exists {
		return nil, fmt.Errorf("backend: default provider %q is not registered", defaultID)
	}
	return registry, nil
}

func (registry *Registry) Lookup(id string) (Driver, error) {
	if registry == nil {
		return nil, errors.New("backend: provider registry is unavailable")
	}
	if id == "" {
		id = registry.defaultID
	}
	driver, exists := registry.drivers[id]
	if !exists {
		return nil, fmt.Errorf("backend: provider %q is not available", id)
	}
	return driver, nil
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
