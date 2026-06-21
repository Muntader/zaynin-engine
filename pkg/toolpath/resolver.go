// Package toolpath resolves external media tool binaries from tools.bin_dir and PATH.
package toolpath

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

var (
	mu     sync.RWMutex
	binDir string
)

// Init sets the base directory for local tool binaries (tools.bin_dir from config).
func Init(dir string) {
	mu.Lock()
	defer mu.Unlock()
	abs, err := filepath.Abs(dir)
	if err != nil {
		binDir = dir
		return
	}
	binDir = abs
}

// BinDir returns the configured tools directory, or empty if Init was not called.
func BinDir() string {
	mu.RLock()
	defer mu.RUnlock()
	return binDir
}

// Resolve finds an executable by name. Lookup order:
//  1. {bin_dir}/{name}
//  2. {bin_dir}/bento4/bin/{name}
//  3. {bin_dir}/packager when name is shaka-packager
//  4. system PATH via exec.LookPath
func Resolve(name string) (string, error) {
	candidates := candidatePaths(name)
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			abs, err := filepath.Abs(p)
			if err != nil {
				return p, nil
			}
			return abs, nil
		}
	}

	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}

	return "", fmt.Errorf(
		"tool %q not found: place it under %s, %s/bento4/bin/, or on PATH",
		name, binDir, binDir,
	)
}

func candidatePaths(name string) []string {
	mu.RLock()
	dir := binDir
	mu.RUnlock()

	if dir == "" {
		return nil
	}

	paths := []string{
		filepath.Join(dir, name),
		filepath.Join(dir, "bento4", "bin", name),
	}
	if name == "shaka-packager" {
		paths = append(paths, filepath.Join(dir, "packager"))
	}
	return paths
}
