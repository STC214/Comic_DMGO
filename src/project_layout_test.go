package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryLayoutPaths(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src")
	exeDir := filepath.Join(root, "bin")
	for _, dir := range []string{filepath.Join(source, "assets"), exeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"go.mod", "config.json", filepath.Join("assets", "example.txt")} {
		p := filepath.Join(source, path)
		if err := os.WriteFile(p, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, start := range []string{root, source, exeDir, filepath.Join(source, "assets")} {
		if got := findProjectRootUpward(start); got != root {
			t.Errorf("start %q: root=%q, want %q", start, got, root)
		}
	}
	for _, wd := range []string{root, source} {
		t.Run(filepath.Base(wd), func(t *testing.T) {
			t.Chdir(wd)
			if got := runtimeRootDir(); got != filepath.Join(root, "runtime") {
				t.Errorf("runtime root=%q", got)
			}
			if got := resourcePath("config.json"); got != filepath.Join(source, "config.json") {
				t.Errorf("config=%q", got)
			}
			if got := resourcePath("assets", "example.txt"); got != filepath.Join(source, "assets", "example.txt") {
				t.Errorf("asset=%q", got)
			}
		})
	}
}

func TestPortableLayoutPathsRemainLocal(t *testing.T) {
	bundle := t.TempDir()
	for _, rel := range []string{"runtime", "workers"} {
		if err := os.Mkdir(filepath.Join(bundle, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Comic_PC.exe", "config.json"} {
		if err := os.WriteFile(filepath.Join(bundle, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(bundle)
	if !looksLikePortableBundle(bundle) || projectRootDir() != bundle {
		t.Fatal("legacy portable bundle no longer resolves its own directory")
	}
	if got := resourcePath("config.json"); got != filepath.Join(bundle, "config.json") {
		t.Errorf("portable config=%q", got)
	}
}
