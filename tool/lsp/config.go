package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const lspConfigFileName = "lsp.json"

type lspServerConfigFile struct {
	Name       string   `json:"name"`
	Command    string   `json:"command"`
	Arguments  []string `json:"arguments"`
	LanguageID string   `json:"languageID"`
	Extensions []string `json:"extensions"`
}

func loadLSPServerConfigs(cwd string) ([]lspServerConfig, error) {
	path := filepath.Join(cwd, lspConfigFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", lspConfigFileName, err)
	}

	var fileConfigs []lspServerConfigFile
	if err := json.Unmarshal(content, &fileConfigs); err != nil {
		return nil, fmt.Errorf("decode %s: %w", lspConfigFileName, err)
	}

	configs := make([]lspServerConfig, len(fileConfigs))
	for index, config := range fileConfigs {
		configs[index] = lspServerConfig{
			name:       config.Name,
			command:    config.Command,
			arguments:  config.Arguments,
			languageID: config.LanguageID,
			extensions: config.Extensions,
		}
	}
	return configs, nil
}
