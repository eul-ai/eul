//go:build !linux && !darwin

package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBashWithoutNetworkFailsClosedOutsideLinuxAndMacOS(t *testing.T) {
	cwd := t.TempDir()
	marker := filepath.Join(cwd, "started")
	result := executeJSON(t, NewBash(cwd), map[string]any{
		"command": `: > "` + marker + `"`,
		"network": false,
	})
	if !result.IsError {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command started without isolation: %v", err)
	}
}
