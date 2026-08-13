package interactive

import (
	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/tool/subagent"
)

func engineOptions(config resolvedConfig, model string, thinkingLevel agent.ThinkingLevel, fastMode, checkpointing bool, inbox agent.InboxSource) agent.Options {
	return agent.Options{
		Model:               model,
		ThinkingLevel:       thinkingLevel,
		FastMode:            fastMode,
		WorkingDirectory:    config.cwd,
		ProjectInstructions: config.projectInstructions,
		Skills:              config.skills,
		Checkpointing:       checkpointing,
		Inbox:               inbox,
	}
}

func childEngineOptions(config resolvedConfig, profile subagent.Profile, thinkingLevel agent.ThinkingLevel, fastMode bool) agent.Options {
	return engineOptions(config, config.models.subagent(profile), thinkingLevel, fastMode, false, nil)
}
