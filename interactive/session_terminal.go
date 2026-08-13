package interactive

import (
	"context"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	"github.com/eul-ai/eul/subagent"
	"github.com/eul-ai/eul/terminal"
)

type sessionModelMetadata struct {
	main          backend.ModelMetadata
	subagent      map[subagent.Profile]backend.ModelMetadata
	thinkingLevel agent.ThinkingLevel
}

type terminalOptionsBuilder struct {
	config             resolvedConfig
	runtime            environment
	metadata           sessionModelMetadata
	warnings           []string
	loadUsage          func(context.Context) (backend.AccountUsage, error)
	subagentUpdates    <-chan subagent.Status
	permissionRequests <-chan terminal.PermissionRequest
	engine             *agent.Engine
	checkpoints        *checkpointCoordinator
	sessions           terminal.Sessions
	initialCheckpoint  *terminal.Checkpoint
	sessionID          string
	previousTurnActive bool
}

func runtimeModelMetadata(backendRuntime backend.Runtime, model string) backend.ModelMetadata {
	metadataProvider, ok := backendRuntime.(backend.ModelMetadataProvider)
	if !ok {
		return backend.ModelMetadata{ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}}
	}
	metadata := metadataProvider.ModelMetadata(model)
	if len(metadata.ThinkingLevels) == 0 {
		metadata.ThinkingLevels = []agent.ThinkingLevel{agent.ThinkingOff}
	}
	return metadata
}

func resolveSessionModelMetadata(backendRuntime backend.Runtime, config resolvedConfig) sessionModelMetadata {
	metadataByModel := make(map[string]backend.ModelMetadata)
	resolve := func(model string) backend.ModelMetadata {
		metadata, ok := metadataByModel[model]
		if !ok {
			metadata = runtimeModelMetadata(backendRuntime, model)
			metadataByModel[model] = metadata
		}
		return metadata
	}

	main := resolve(config.models.primary)
	return sessionModelMetadata{
		main: main,
		subagent: map[subagent.Profile]backend.ModelMetadata{
			subagent.ProfileFast:     resolve(config.models.forProfile(subagent.ProfileFast)),
			subagent.ProfileBalanced: resolve(config.models.forProfile(subagent.ProfileBalanced)),
			subagent.ProfilePowerful: resolve(config.models.forProfile(subagent.ProfilePowerful)),
		},
		thinkingLevel: main.ClampThinkingLevel(config.thinkingLevel),
	}
}

func (source terminalOptionsBuilder) options() terminal.Options {
	var loadUsage func(context.Context) (terminal.ProviderUsage, error)
	if source.loadUsage != nil {
		loadUsage = func(ctx context.Context) (terminal.ProviderUsage, error) {
			usage, err := source.loadUsage(ctx)
			windows := make([]terminal.UsageWindow, len(usage.Windows))
			for index, window := range usage.Windows {
				windows[index] = terminal.UsageWindow{
					Duration:    window.Duration,
					UsedPercent: window.UsedPercent,
					ResetsAt:    window.ResetsAt,
				}
			}
			return terminal.ProviderUsage{Windows: windows}, err
		}
	}
	callbacks := newTerminalCallbacks(source.engine, source.checkpoints)
	return terminal.Options{
		Input:      source.runtime.stdin,
		Output:     source.runtime.stdout,
		Operations: callbacks.operations(),
		Controls: terminal.Controls{
			Steer:            source.engine.Steer,
			ClearSteering:    source.engine.ClearSteering,
			SetGoal:          source.engine.SetGoal,
			Goal:             source.engine.Goal,
			ClearGoal:        source.engine.ClearGoal,
			SetThinkingLevel: source.engine.SetThinkingLevel,
			SetFastMode: func(enabled bool) error {
				source.engine.SetFastMode(enabled)
				return nil
			},
		},
		Sessions:     source.sessions,
		StateChanges: terminalStateChanges(source.checkpoints),
		Config: terminal.Config{
			Model:              source.config.models.primary,
			WorkingDirectory:   source.config.cwd,
			ThinkingLevel:      source.metadata.thinkingLevel,
			ThinkingLevels:     source.metadata.main.ThinkingLevels,
			FastMode:           source.config.fastMode,
			FastModeAvailable:  source.metadata.main.FastMode,
			ContextWindow:      source.metadata.main.ContextWindow,
			Skills:             source.config.skills,
			Warnings:           source.warnings,
			InitialCheckpoint:  source.initialCheckpoint,
			SessionID:          source.sessionID,
			PreviousTurnActive: source.previousTurnActive,
		},
		Events: terminal.Events{
			Interrupts:         source.runtime.interrupts,
			SubagentUpdates:    source.subagentUpdates,
			PermissionRequests: source.permissionRequests,
		},
		Services: terminal.Services{LoadUsage: loadUsage},
	}
}

func terminalStateChanges(checkpoints *checkpointCoordinator) terminal.StateChanges {
	if checkpoints == nil {
		return terminal.StateChanges{}
	}
	return terminal.StateChanges{Notify: checkpoints.SaveTerminalCheckpoint}
}

func runtimeUsageLoader(backendRuntime backend.Runtime) func(context.Context) (backend.AccountUsage, error) {
	usageProvider, ok := backendRuntime.(backend.UsageProvider)
	if !ok {
		return nil
	}
	return usageProvider.Usage
}
