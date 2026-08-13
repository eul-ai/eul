package agent

import (
	"errors"
	"sync"
)

type Settings struct {
	mu            sync.RWMutex
	thinkingLevel ThinkingLevel
	fastMode      bool
}

func NewSettings(thinkingLevel ThinkingLevel, fastMode bool) *Settings {
	if thinkingLevel == "" {
		thinkingLevel = DefaultThinkingLevel
	}
	return &Settings{thinkingLevel: thinkingLevel, fastMode: fastMode}
}

func (settings *Settings) SetThinkingLevel(level ThinkingLevel) error {
	if !level.Valid() {
		return errors.New("agent: invalid thinking level")
	}
	settings.mu.Lock()
	settings.thinkingLevel = level
	settings.mu.Unlock()
	return nil
}

func (settings *Settings) SetFastMode(enabled bool) {
	settings.mu.Lock()
	settings.fastMode = enabled
	settings.mu.Unlock()
}

func (settings *Settings) Snapshot() (ThinkingLevel, bool) {
	settings.mu.RLock()
	defer settings.mu.RUnlock()
	return settings.thinkingLevel, settings.fastMode
}
