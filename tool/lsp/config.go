package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const configFileName = "lsp.json"

type serverConfigFile struct {
	Name       string   `json:"name"`
	Command    string   `json:"command"`
	Arguments  []string `json:"arguments"`
	LanguageID string   `json:"languageID"`
	Extensions []string `json:"extensions"`
}

func loadServerConfigs(paths ...string) ([]serverConfig, error) {
	for _, configPath := range paths {
		content, err := os.ReadFile(configPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s (%s): %w", configFileName, filepath.ToSlash(configPath), err)
		}

		var fileConfigs []serverConfigFile
		if err := json.Unmarshal(content, &fileConfigs); err != nil {
			return nil, fmt.Errorf("decode %s (%s): %w", configFileName, filepath.ToSlash(configPath), err)
		}
		configs, err := validateLSPServerConfigs(fileConfigs)
		if err != nil {
			return nil, fmt.Errorf("validate %s (%s): %w", configFileName, filepath.ToSlash(configPath), err)
		}
		return configs, nil
	}

	return nil, nil
}

func validateLSPServerConfigs(fileConfigs []serverConfigFile) ([]serverConfig, error) {
	configs := make([]serverConfig, len(fileConfigs))
	names := make(map[string]struct{}, len(fileConfigs))
	extensions := make(map[string]string)
	for index, config := range fileConfigs {
		switch {
		case !validLSPConfigValue(config.Name):
			return nil, fmt.Errorf("server %d has an invalid name", index+1)
		case !validLSPConfigValue(config.Command):
			return nil, fmt.Errorf("server %q has an invalid command", config.Name)
		case !validLSPConfigValue(config.LanguageID):
			return nil, fmt.Errorf("server %q has an invalid languageID", config.Name)
		case len(config.Extensions) == 0:
			return nil, fmt.Errorf("server %q has no extensions", config.Name)
		}
		if _, exists := names[config.Name]; exists {
			return nil, fmt.Errorf("server name %q is duplicated", config.Name)
		}
		names[config.Name] = struct{}{}

		normalizedExtensions := make([]string, len(config.Extensions))
		for extensionIndex, extension := range config.Extensions {
			extension = strings.ToLower(extension)
			if !validLSPExtension(extension) {
				return nil, fmt.Errorf("server %q has invalid extension %q", config.Name, config.Extensions[extensionIndex])
			}
			if owner, exists := extensions[extension]; exists {
				return nil, fmt.Errorf("extension %q is configured for both %q and %q", extension, owner, config.Name)
			}
			extensions[extension] = config.Name
			normalizedExtensions[extensionIndex] = extension
		}

		configs[index] = serverConfig{
			name:       config.Name,
			command:    config.Command,
			arguments:  append([]string(nil), config.Arguments...),
			languageID: config.LanguageID,
			extensions: normalizedExtensions,
		}
	}
	return configs, nil
}

func validLSPConfigValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validLSPExtension(extension string) bool {
	return len(extension) > 1 && extension[0] == '.' && strings.TrimSpace(extension) == extension &&
		!strings.ContainsAny(extension, `/\`) && strings.IndexFunc(extension, unicode.IsControl) < 0
}
