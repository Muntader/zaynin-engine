package packager

import (
	"os"
	"path/filepath"
)

func (a AudioTrackInfo) toPackagerType() AudioTrackInfo {
	return AudioTrackInfo(a)
}

func (s SubtitleTrackInfo) toPackagerType() SubtitleTrackInfo {
	return SubtitleTrackInfo(s)
}

// FindMediaFiles lists files with a given ext in a directory (non-recursive).
func FindMediaFiles(dir, ext string) ([]string, error) {
	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ext {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	return files, nil
}
