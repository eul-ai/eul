package openrouter

import "github.com/eul-ai/eul/agent"

type modelCatalog struct {
	Data []modelDescription `json:"data"`
}

type modelDescription struct {
	ID            string         `json:"id"`
	ContextLength int64          `json:"context_length"`
	Reasoning     modelReasoning `json:"reasoning"`
}

type modelReasoning struct {
	Mandatory        bool     `json:"mandatory"`
	SupportedEfforts []string `json:"supported_efforts"`
	DefaultEffort    string   `json:"default_effort"`
}

type modelMetadata struct {
	contextWindow        int64
	reasoning            bool
	thinkingLevels       []agent.ThinkingLevel
	defaultThinkingLevel agent.ThinkingLevel
}
