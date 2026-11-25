package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The web UI authenticates with an httpOnly session cookie rather than the
// bearer token itself: a token pasted into localStorage is one XSS away from
// being exfiltrated, and the token is the whole admin API.
const (
	sessionCookie = "fleet_session"
	sessionTTL    = 12 * time.Hour
)

// sessionKey derives the cookie key from the admin token, so rotating the token
// invalidates every existing session, and no extra secret has to be configured.
func (s *Server) sessionKey() []byte {
	sum := sha256.Sum256([]byte("fleet-session-v1|" + s.cfg.AdminToken))
	return sum[:]
}

func (s *Server) signSession(payload string) string {
	mac := hmac.New(sha256.New, s.sessionKey())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request) {
	exp := strconv.FormatInt(s.now().Add(sessionTTL).Unix(), 10)
	val := exp + "." + s.signSession(exp)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    val,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  s.now().Add(sessionTTL),
	})
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// authed reports whether the request carries a valid session cookie.
func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	payload, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(sig), []byte(s.signSession(payload))) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return s.now().Unix() < exp
}

// tokenMatches compares the submitted admin token in constant time.
func (s *Server) tokenMatches(got string) bool {
	if s.cfg.AdminToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AdminToken)) == 1
}

// sameOrigin guards state-changing form posts. The session cookie is
// SameSite=Strict, so a browser will not send it cross-site; this is the second
// lock, and it also stops a token-bearing form from another origin.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a browser form post
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
