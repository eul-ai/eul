package chatcompletions

import (
	"time"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

func (client *Client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return backendhttp.RetryGeneration(err, failedAttempts)
}

var (
	_ agent.Provider              = (*Client)(nil)
	_ agent.GenerationRetryPolicy = (*Client)(nil)
)
