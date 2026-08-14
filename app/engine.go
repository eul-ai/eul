package app

import (
	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/subagent"
)

func engineOptions(config resolvedConfig, model string, settings *agent.Settings, checkpointing bool, inbox agent.InboxSource, additionalInstructions func() string) agent.Options {
	return agent.Options{
		Model:                  model,
		Settings:               settings,
		WorkingDirectory:       config.cwd,
		ProjectInstructions:    config.projectInstructions,
		Skills:                 config.skills,
		Checkpointing:          checkpointing,
		Inbox:                  inbox,
		AdditionalInstructions: additionalInstructions,
	}
}

func childEngineOptions(config resolvedConfig, profile subagent.Profile, thinkingLevel agent.ThinkingLevel, fastMode bool) agent.Options {
	return engineOptions(config, config.models.forProfile(profile), agent.NewSettings(thinkingLevel, fastMode), false, nil, nil)
}
