package textfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplacePreservesModeAndSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.txt")
	link := filepath.Join(directory, "link.txt")
	if err := os.WriteFile(target, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	snapshot, err := Load(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := Replace(snapshot, []byte("after")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(link)
	if err != nil || string(content) != "after" {
		t.Fatalf("content=%q error=%v", content, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%v error=%v", info.Mode(), err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link mode=%v error=%v", info.Mode(), err)
	}
}

func TestReplaceRejectsChangedFileAndRemovesTemporary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Replace(snapshot, []byte("after")); !errors.Is(err, ErrChanged) {
		t.Fatalf("Replace() error = %v", err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "external" {
		t.Fatalf("content = %q", content)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".eul-replace-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files=%v error=%v", matches, err)
	}
}

func TestReplaceRejectsChangedMetadataAndIdentity(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sample.txt")
		if err := os.WriteFile(path, []byte("before"), 0o640); err != nil {
			t.Fatal(err)
		}
		snapshot, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Replace(snapshot, []byte("after")); !errors.Is(err, ErrChanged) {
			t.Fatalf("Replace error = %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o", info.Mode().Perm())
		}
	})

	t.Run("identity", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "sample.txt")
		if err := os.WriteFile(path, []byte("before"), 0o640); err != nil {
			t.Fatal(err)
		}
		snapshot, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		replacement := filepath.Join(directory, "replacement.txt")
		if err := os.WriteFile(replacement, []byte("before"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		if err := Replace(snapshot, []byte("after")); !errors.Is(err, ErrChanged) {
			t.Fatalf("Replace error = %v", err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "before" {
			t.Fatalf("content = %q", content)
		}
	})
}

func TestLoadRejectsNonTextAndNonRegularFiles(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "binary")
	if err := os.WriteFile(binary, []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(binary); err == nil {
		t.Fatal("Load() accepted binary content")
	}
	if _, err := Load(directory); err == nil {
		t.Fatal("Load() accepted a directory")
	}
}
