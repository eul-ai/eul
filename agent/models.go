package agent

const gpt56ContextWindow = 272_000

var modelContextWindows = map[string]int{
	"gpt-5.6-luna":  gpt56ContextWindow,
	"gpt-5.6-sol":   gpt56ContextWindow,
	"gpt-5.6-terra": gpt56ContextWindow,
}
