package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRelativePath(t *testing.T) {
	ok := []string{"track.flac", "imports/example/track.flac", "imports/example/song.part.flac"}
	for _, path := range ok {
		if err := ValidateRelativePath(path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	bad := []string{"", "/abs.flac", `imports\track.flac`, "imports/../../outside.mp3", "imports/./track.flac", ".", "..", "imports/track.flac.part", "imports//track.flac"}
	for _, path := range bad {
		if err := ValidateRelativePath(path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}

func TestAcceptImportPaths(t *testing.T) {
	library := t.TempDir()
	dest := filepath.Join(library, "imports", "example")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "track.flac"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(library, "imports", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "track.flac"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := AcceptImportPaths(library, "imports/example", []string{"imports/example/track.flac"})
	if err != nil || len(got) != 1 || got[0].Relative != "imports/example/track.flac" {
		t.Fatalf("accepted = %#v, %v", got, err)
	}
	if _, err := AcceptImportPaths(library, "imports/example", []string{"imports/other/track.flac"}); err == nil || err.Error() != "result path is outside the destination" {
		t.Fatalf("other dest err = %v", err)
	}
	if _, err := AcceptImportPaths(library, "imports/example", []string{"imports/example/missing.flac"}); err == nil || err.Error() != "result file is missing" {
		t.Fatalf("missing err = %v", err)
	}
}

func TestAcceptImportPathsRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(filepath.Join(library, "imports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(library, "imports", "example")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "track.flac"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := AcceptImportPaths(library, "imports/example", []string{"imports/example/track.flac"})
	if err == nil || err.Error() != "result path is outside the library" {
		t.Fatalf("symlink err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "track.flac")); statErr != nil {
		t.Fatal(statErr)
	}
}
