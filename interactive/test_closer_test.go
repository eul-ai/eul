package interactive

import "github.com/eul-ai/eul/tool"

type lifecycleToolSet struct {
	closer *lifecycleCloser
}

func (set lifecycleToolSet) Tools() []tool.Tool { return nil }
func (set lifecycleToolSet) Close() error       { return set.closer.Close() }
