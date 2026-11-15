package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ValidateRelativePath checks the protocol v1 relative-path grammar.
func ValidateRelativePath(value string) error {
	if !validRelativePath(value) {
		return failure(CodeInvalidRequest, "path is invalid")
	}
	return nil
}

// ResolvedPath is a library-relative path and the file it resolved to.
type ResolvedPath struct {
	Relative string
	Absolute string
}

// AcceptImportPaths checks that every path is inside library and destDir,
// and that each resolved target is a regular file.
func AcceptImportPaths(library, destDir string, paths []string) ([]ResolvedPath, error) {
	if len(paths) < 1 || len(paths) > maxResultPaths {
		return nil, failure(CodeInternal, "result paths are invalid")
	}
	for _, rel := range paths {
		if err := ValidateRelativePath(rel); err != nil {
			return nil, failure(CodeInternal, "result path is invalid")
		}
		if !underDest(destDir, rel) {
			return nil, failure(CodeInternal, "result path is outside the destination")
		}
	}
	root, err := resolveDirectory(library)
	if err != nil {
		return nil, failure(CodeInternal, "result path is outside the library")
	}
	destAbs, err := resolveExisting(filepath.Join(root, filepath.FromSlash(destDir)))
	if err != nil || !strictlyInside(root, destAbs) {
		return nil, failure(CodeInternal, "result path is outside the library")
	}
	out := make([]ResolvedPath, 0, len(paths))
	for _, rel := range paths {
		abs, err := resolveExisting(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, failure(CodeInternal, "result file is missing")
			}
			return nil, failure(CodeInternal, "result path is invalid")
		}
		if !strictlyInside(root, abs) {
			return nil, failure(CodeInternal, "result path is outside the library")
		}
		if !strictlyInside(destAbs, abs) {
			return nil, failure(CodeInternal, "result path is outside the destination")
		}
		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return nil, failure(CodeInternal, "result path is invalid")
		}
		out = append(out, ResolvedPath{Relative: rel, Absolute: abs})
	}
	return out, nil
}

func underDest(destDir, rel string) bool {
	return strings.HasPrefix(rel, destDir+"/")
}

func validLibraryPath(path string) bool {
	if path == "" || runeCount(path) > maxLibraryChars {
		return false
	}
	return libraryPattern.MatchString(path)
}

func normalizeLibrary(path string) (string, error) {
	if !validLibraryPath(path) {
		return "", failure(CodeInvalidRequest, "library path is invalid")
	}
	root, err := resolveDirectory(path)
	if err != nil || !validLibraryPath(root) {
		return "", failure(CodeInvalidRequest, "library path is invalid")
	}
	return root, nil
}

func resolveDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", os.ErrNotExist
	}
	return resolved, nil
}

func resolveExisting(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func strictlyInside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
