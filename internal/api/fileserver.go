package api

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// LiveDashHandler accepts LL-DASH segment PUTs (chunked) into rootDir.
func LiveDashHandler(rootDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			slog.Warn("Invalid method for LL-DASH ingest", "method", r.Method)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		relativePath := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")
		fullDiskPath := filepath.Join(rootDir, relativePath)

		absRootDir, err := filepath.Abs(rootDir)
		if err != nil {
			slog.Error("Could not determine absolute path for root directory", "root", rootDir, "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		absFilePath, err := filepath.Abs(fullDiskPath)
		if err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}

		if !strings.HasPrefix(absFilePath, absRootDir) {
			slog.Warn("Path traversal attempt blocked during ingest", "path", r.URL.Path, "root", rootDir)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		dir := filepath.Dir(fullDiskPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			slog.Error("Failed to create directory for LL-DASH segment", "dir", dir, "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		file, err := os.Create(fullDiskPath)
		if err != nil {
			slog.Error("Failed to create file for LL-DASH segment", "path", fullDiskPath, "error", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		defer file.Close()

		_, err = io.Copy(file, r.Body)
		if err != nil {
			slog.Error("Failed to write LL-DASH segment", "path", fullDiskPath, "error", err)
			// client may have dropped   no point writing an HTTP error back
			return
		}

		w.WriteHeader(http.StatusCreated)
	})
}

// serveMediaFile resolves a URL path under rootDir and serves the file if it exists.
func serveMediaFile(w http.ResponseWriter, r *http.Request, rootDir string) {
	relativePath := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")

	fullDiskPath := filepath.Join(rootDir, relativePath)

	absRootDir, err := filepath.Abs(rootDir)
	if err != nil {
		slog.Error("Could not determine absolute path for root directory", "root", rootDir, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	absFilePath, err := filepath.Abs(fullDiskPath)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(absFilePath, absRootDir) {
		slog.Warn("Path traversal attempt blocked", "path", r.URL.Path, "root", rootDir)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	fileInfo, err := os.Stat(fullDiskPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		slog.Error("Error stating file", "path", fullDiskPath, "error", err)
		return
	}

	if fileInfo.IsDir() {
		slog.Warn("Directory listing attempt blocked", "path", r.URL.Path)
		http.NotFound(w, r)
		return
	}

	contentType := getContentType(fullDiskPath)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Access-Control-Allow-Origin", "*")

	http.ServeFile(w, r, fullDiskPath)
}

func PublicMediaFileServer(rootDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveMediaFile(w, r, rootDir)
	})
}

// ProtectedMediaFileServer requires a JWT whose stream_id claim matches the URL path.
func ProtectedMediaFileServer(rootDir, jwtSecret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenString := r.URL.Query().Get("token")
		if tokenString == "" {
			slog.Warn("Protected playback access denied: No token provided.", "path", r.URL.Path)
			http.Error(w, "Forbidden: No token provided", http.StatusForbidden)
			return
		}

		claims := &jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
			return []byte(jwtSecret), nil
		})

		if err != nil || !token.Valid {
			slog.Error("Protected playback access denied: Invalid token", "path", r.URL.Path, "error", err)
			http.Error(w, "Unauthorized: Invalid token", http.StatusUnauthorized)
			return
		}

		pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(pathParts) < 1 {
			slog.Warn("Protected playback access denied: Malformed path.", "path", r.URL.Path)
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}

		urlStreamID := pathParts[0]
		tokenStreamID, ok := (*claims)["stream_id"].(string)
		if !ok || tokenStreamID != urlStreamID {
			slog.Warn("Protected playback access denied: Token stream ID does not match URL stream ID.", "path", r.URL.Path, "token_stream_id", tokenStreamID, "url_stream_id", urlStreamID)
			http.Error(w, "Forbidden: Token mismatch", http.StatusForbidden)
			return
		}

		serveMediaFile(w, r, rootDir)
	})
}

func getContentType(filePath string) string {
	switch {
	case strings.HasSuffix(filePath, ".m3u8"):
		return "application/vnd.apple.mpegurl"
	case strings.HasSuffix(filePath, ".ts"):
		return "video/mp2t"
	case strings.HasSuffix(filePath, ".mpd"):
		return "application/dash+xml"
	case strings.HasSuffix(filePath, ".m4s"), strings.HasSuffix(filePath, ".mp4"):
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}
