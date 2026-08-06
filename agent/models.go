package agent

const gpt56ContextWindow = 272_000

// TODO: Import model context windows from https://pi.dev/api/models/providers/openai-codex.
var modelContextWindows = map[string]int{
	"gpt-5.6-luna":  gpt56ContextWindow,
	"gpt-5.6-sol":   gpt56ContextWindow,
	"gpt-5.6-terra": gpt56ContextWindow,
}
