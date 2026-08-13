package openrouter

type modelCatalog struct {
	Data []modelDescription `json:"data"`
}

type modelDescription struct {
	ID                  string   `json:"id"`
	ContextLength       int64    `json:"context_length"`
	SupportedParameters []string `json:"supported_parameters"`
}

type modelMetadata struct {
	contextWindow int64
	reasoning     bool
}
