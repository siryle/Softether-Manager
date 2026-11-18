package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type loginAttempt struct {
	count   int
	expires time.Time
}
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func loginIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func (l *loginLimiter) take(ip string, at time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = make(map[string]loginAttempt)
	}
	for key, attempt := range l.attempts {
		if !at.Before(attempt.expires) {
			delete(l.attempts, key)
		}
	}
	attempt, exists := l.attempts[ip]
	if !exists {
		if len(l.attempts) >= 4096 {
			var oldestKey string
			var oldestTime time.Time
			for k, v := range l.attempts {
				if oldestTime.IsZero() || v.expires.Before(oldestTime) {
					oldestKey = k
					oldestTime = v.expires
				}
			}
			if oldestKey != "" {
				delete(l.attempts, oldestKey)
			}
		}
		attempt.expires = at.Add(5 * time.Minute)
	}
	if attempt.count >= 8 {
		return false
	}
	attempt.count++
	l.attempts[ip] = attempt
	return true
}
func (l *loginLimiter) clear(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}
func requestToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	if cookie, err := r.Cookie("sem_session"); err == nil {
		return cookie.Value
	}
	return ""
}
func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
func (a *App) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	for _, allowed := range a.cfg.CORS {
		if origin == strings.TrimSpace(allowed) {
			return true
		}
	}
	return false
}
