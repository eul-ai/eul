package agent

// TODO: Import model context windows from https://pi.dev/api/models/providers/openai-codex.
//
//lint:ignore U1000 Reserved for automatic compaction.
var modelContextWindows = map[string]int{
	"gpt-5.6-luna":  272_000,
	"gpt-5.6-sol":   272_000,
	"gpt-5.6-terra": 272_000,
}
