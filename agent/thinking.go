package agent

import (
	"errors"
	"slices"
)

type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"

	DefaultThinkingLevel = ThinkingMedium
)

var thinkingLevels = []ThinkingLevel{
	ThinkingOff,
	ThinkingMinimal,
	ThinkingLow,
	ThinkingMedium,
	ThinkingHigh,
	ThinkingXHigh,
	ThinkingMax,
}

type ThinkingLevelMap map[ThinkingLevel]string

func ParseThinkingLevel(value string) (ThinkingLevel, error) {
	level := ThinkingLevel(value)
	if !level.Valid() {
		return "", errors.New("thinking level must be one of off, minimal, low, medium, high, xhigh, or max")
	}
	return level, nil
}

func (level ThinkingLevel) Valid() bool {
	return slices.Contains(thinkingLevels, level)
}

func ThinkingLevels() []ThinkingLevel {
	return slices.Clone(thinkingLevels)
}

func (levels ThinkingLevelMap) SupportedLevels() []ThinkingLevel {
	supported := make([]ThinkingLevel, 0, len(levels))
	for _, level := range thinkingLevels {
		if _, ok := levels[level]; ok {
			supported = append(supported, level)
		}
	}
	return supported
}

func (levels ThinkingLevelMap) Clamp(level ThinkingLevel) ThinkingLevel {
	if _, ok := levels[level]; ok {
		return level
	}

	supported := levels.SupportedLevels()
	requested := slices.Index(thinkingLevels, level)
	if requested < 0 {
		if len(supported) > 0 {
			return supported[0]
		}
		return ThinkingOff
	}
	for index := requested + 1; index < len(thinkingLevels); index++ {
		if _, ok := levels[thinkingLevels[index]]; ok {
			return thinkingLevels[index]
		}
	}
	for index := requested - 1; index >= 0; index-- {
		if _, ok := levels[thinkingLevels[index]]; ok {
			return thinkingLevels[index]
		}
	}
	return ThinkingOff
}
