package apitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlayerAndPageStayFreeOfSiteDownloaders(t *testing.T) {
	root := repoRoot(t)
	banned := []string{
		"you" + "tube",
		"you" + "tu.be",
		"yt" + "-dlp",
		"yt_" + "dlp",
		"google" + "video.com",
		"you" + "tube-nocookie.com",
	}
	var hits []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			switch {
			case name == ".git" || name == "__pycache__" || name == "node_modules" || name == "data" || name == "dist" || name == "venv":
				return filepath.SkipDir
			case strings.HasPrefix(name, ".venv"):
				return filepath.SkipDir
			}
			return nil
		}
		if !scanText(entry.Name()) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := strings.ToLower(string(data))
		for _, word := range banned {
			if strings.Contains(text, word) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, rel+": "+word)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("site-specific downloader names in the tree:\n%s", strings.Join(hits, "\n"))
	}

	page, err := os.ReadFile(filepath.Join(root, "player", "internal", "static", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(page), "/* plugin-panels */")
	end := strings.Index(string(page), "/* /plugin-panels */")
	if start < 0 || end < start {
		t.Fatal("plugin panel is missing from the page")
	}
	panel := string(page[start:end])
	if !strings.Contains(panel, "library.search") || !strings.Contains(panel, "В библиотеку") {
		t.Fatal("plugin panel does not render search and import from the protocol")
	}
	if strings.Contains(panel, "href") || strings.Contains(panel, `createElement("a")`) {
		t.Fatal("plugin panel opens an address as a link")
	}
}

func scanText(name string) bool {
	if strings.HasPrefix(name, "Dockerfile") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".py", ".js", ".html", ".css", ".md", ".json", ".yml", ".yaml", ".toml", ".sql", ".txt", ".mod", ".sum", ".svg", ".example":
		return true
	default:
		return false
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "sdk", "plugin", "v1")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "player", "go.mod")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}
