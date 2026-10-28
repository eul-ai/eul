package responses

import (
	"time"

	"github.com/eul-ai/eul/agent"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

func (c *Client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return backendhttp.RetryGeneration(err, failedAttempts)
}

var _ agent.GenerationRetryPolicy = (*Client)(nil)
