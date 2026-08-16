package messages

import (
	"encoding/json"
	"fmt"
)

func withPromptCacheControl(request createRequest) (createRequest, error) {
	request.Tools = append([]toolDefinition(nil), request.Tools...)
	request.System = append([]systemBlock(nil), request.System...)
	request.Messages = append([]json.RawMessage(nil), request.Messages...)

	if len(request.Tools) != 0 {
		request.Tools[len(request.Tools)-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	if len(request.System) != 0 {
		request.System[len(request.System)-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}

	start := max(0, len(request.Messages)-2)
	for index := start; index < len(request.Messages); index++ {
		var message wireMessage
		if err := json.Unmarshal(request.Messages[index], &message); err != nil {
			return createRequest{}, fmt.Errorf("decode message %d for prompt caching: %w", index, err)
		}

		var blocks []contentBlock
		if err := json.Unmarshal(message.Content, &blocks); err != nil {
			return createRequest{}, fmt.Errorf("decode message %d content for prompt caching: %w", index, err)
		}
		if len(blocks) == 0 {
			continue
		}
		blocks[len(blocks)-1].CacheControl = &cacheControl{Type: "ephemeral"}

		content, err := json.Marshal(blocks)
		if err != nil {
			return createRequest{}, fmt.Errorf("encode message %d content for prompt caching: %w", index, err)
		}
		message.Content = content
		request.Messages[index], err = json.Marshal(message)
		if err != nil {
			return createRequest{}, fmt.Errorf("encode message %d for prompt caching: %w", index, err)
		}
	}

	return request, nil
}
