package utils

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SecureJoin blocks .. escapes when joining paths under a media root.
func SecureJoin(base, target string) (string, error) {
	finalPath := filepath.Join(base, target)
	rel, err := filepath.Rel(base, finalPath)
	if err != nil {
		return "", fmt.Errorf("failed to calculate relative path: %w", err)
	}

	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path traversal attempt detected")
	}

	return finalPath, nil
}
