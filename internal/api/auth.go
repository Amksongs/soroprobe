package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireToken returns middleware that gates every request on a matching
// bearer token.
//
// A missing or malformed Authorization header and a wrong token are both a
// 401: either way the client does not hold a credential that works. The two
// cases get distinct error bodies because they tell an operator different
// things — a client that sends no header is not configured, one that sends a
// wrong token is misconfigured — and neither leaks the token itself.
//
// The comparison is constant-time so that response timing cannot be used to
// discover the configured token one character at a time. A token is compared
// in full even after a mismatch, rather than returning early.
func requireToken(token string) func(http.Handler) http.Handler {
	expected := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			presented, ok := bearerToken(r)
			if !ok {
				unauthorized(w, "missing bearer token")
				return
			}
			if subtle.ConstantTimeCompare([]byte(presented), expected) != 1 {
				unauthorized(w, "invalid bearer token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the credential from an Authorization header. The
// scheme is matched case-insensitively, as RFC 7235 requires of auth schemes.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

func unauthorized(w http.ResponseWriter, detail string) {
	// WWW-Authenticate tells a client how to authenticate, per RFC 7235. It
	// is sent on 401 only; there is no 403 here, because an unauthenticated
	// caller never gets far enough to be told "known, but no".
	w.Header().Set("WWW-Authenticate", `Bearer realm="soroprobe"`)
	writeJSON(w, http.StatusUnauthorized, errorResponse{Error: detail})
}
