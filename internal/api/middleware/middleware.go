package middleware

import (
	"crypto/subtle"
	"github.com/muntader/zaynin-engine/internal/api/helpers"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// PanicRecovery keeps a rogue handler from taking down the whole process.
func PanicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("A panic occurred in an HTTP handler", "error", err, "stack", string(debug.Stack()))

				if w.Header().Get("Content-Type") == "" {
					helpers.WriteAPIError(w, http.StatusInternalServerError, "An internal server error occurred", nil)
				}
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func APIKeyAuth(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			providedKey := r.Header.Get("X-API-Key")
			if providedKey == "" {
				helpers.WriteAPIError(w, http.StatusForbidden, "No API key provided", nil)
				return
			}

			// constant-time compare so callers cant guess the key byte-by-byte
			if subtle.ConstantTimeCompare([]byte(providedKey), []byte(apiKey)) != 1 {
				helpers.WriteAPIError(w, http.StatusForbidden, "Invalid API key", nil)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
