package tool

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestTextFileRejectsOversizedAndNonRegularFiles(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "oversized.txt")
	oversized := bytes.Repeat([]byte("x"), maxTextFileBytes+1)
	if err := os.WriteFile(path, oversized, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadText(path); err == nil {
		t.Fatal("loadText accepted an oversized file")
	}
	if _, err := loadText(directory); err == nil {
		t.Fatal("loadText accepted a directory")
	}
	if err := replaceText(textSnapshot{path: path, mode: 0o644}, oversized); err == nil {
		t.Fatal("replaceText accepted oversized content")
	}
}
