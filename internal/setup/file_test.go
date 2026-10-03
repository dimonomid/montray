package setup

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureFileDoesNotOverwriteExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "montray.yml")
	created, err := EnsureFile(path, "first\n")
	if err != nil || !created {
		t.Fatalf("EnsureFile() = (%v, %v), want (true, nil)", created, err)
	}

	created, err = EnsureFile(path, "second\n")
	if err != nil || created {
		t.Fatalf("EnsureFile() = (%v, %v), want (false, nil)", created, err)
	}
	data, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "first\n"; got != want {
		t.Fatalf("config contents = %q, want %q", got, want)
	}
	entries, err := ioutil.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "montray.yml" {
		t.Fatalf("directory entries = %#v, want only montray.yml", entries)
	}
}

func TestReplaceFileOverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed", "file")
	created, err := ReplaceFile(path, "first\n", 0640)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("ReplaceFile() did not report creating a new file")
	}

	created, err = ReplaceFile(path, "second\n", 0640)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("ReplaceFile() reported replacing an existing file as new")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "second\n"; got != want {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0640); got != want {
		t.Fatalf("file mode = %o, want %o", got, want)
	}
}
