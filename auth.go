package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// AuthInfo is injected into request context by the auth middleware.
type AuthInfo struct {
	IsLoggedIn bool
	IsAdmin    bool
	Email      string
	Name       string
	Groups     []string
}

type contextKey string

const authContextKey contextKey = "auth"

func authInfoFromContext(ctx context.Context) AuthInfo {
	if ai, ok := ctx.Value(authContextKey).(AuthInfo); ok {
		return ai
	}
	return AuthInfo{}
}

// authConfig holds OIDC configuration. If ClientID is empty, auth is disabled.
type authConfig struct {
	ClientID     string
	ClientSecret string
	IssuerURL    string
	BaseURL      string
	SecureCookie bool
}

func (c authConfig) enabled() bool {
	return c.ClientID != "" && c.ClientSecret != ""
}

func (c authConfig) redirectURI() string {
	return strings.TrimRight(c.BaseURL, "/") + "/auth/callback"
}

func (c authConfig) oauth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  c.IssuerURL + "/authorize",
			TokenURL: c.IssuerURL + "/api/oidc/token",
		},
		RedirectURL: c.redirectURI(),
		Scopes:      []string{"openid", "profile", "email", "groups"},
	}
}

// session represents an authenticated user session.
type session struct {
	Email     string
	Name      string
	Groups    []string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// sessionStore is a thread-safe in-memory session store.
type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]session // sessionID -> session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]session)}
}

func (s *sessionStore) create(sess session) (string, error) {
	id, err := randomString(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	return id, nil
}

func (s *sessionStore) get(id string) (session, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	if ok && time.Now().After(sess.ExpiresAt) {
		s.delete(id)
		return session{}, false
	}
	return sess, ok
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// pendingAuth stores OIDC state + PKCE verifier for in-flight auth requests.
type pendingAuth struct {
	Verifier    string
	RedirectURL string
	ExpiresAt   time.Time
}

// authState stores pending OIDC auth flows keyed by state parameter.
type authState struct {
	mu      sync.Mutex
	pending map[string]pendingAuth
}

func newAuthState() *authState {
	return &authState{pending: make(map[string]pendingAuth)}
}

func (a *authState) store(state string, pa pendingAuth) {
	a.mu.Lock()
	// Lazy cleanup
	now := time.Now()
	for k, v := range a.pending {
		if now.After(v.ExpiresAt) {
			delete(a.pending, k)
		}
	}
	a.pending[state] = pa
	a.mu.Unlock()
}

func (a *authState) consume(state string) (pendingAuth, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pa, ok := a.pending[state]
	if !ok {
		return pendingAuth{}, false
	}
	delete(a.pending, state)
	if time.Now().After(pa.ExpiresAt) {
		return pendingAuth{}, false
	}
	return pa, true
}

// oidcUserInfo is the response from the userinfo endpoint.
type oidcUserInfo struct {
	Email  string   `json:"email"`
	Name   string   `json:"name"`
	Groups []string `json:"groups"`
}

// authMiddleware injects AuthInfo into the request context.
func (s *server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ai := AuthInfo{}
		if cookie, err := r.Cookie("recipes-session"); err == nil {
			if sess, ok := s.sessions.get(cookie.Value); ok {
				ai = AuthInfo{
					IsLoggedIn: true,
					IsAdmin:    slices.Contains(sess.Groups, "recipes_admin"),
					Email:      sess.Email,
					Name:       sess.Name,
					Groups:     sess.Groups,
				}
			}
		}
		ctx := context.WithValue(r.Context(), authContextKey, ai)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requiresAdmin wraps a handler to require admin access.
func (s *server) requiresAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If auth is disabled, allow everything (backward compat)
		if !s.auth.enabled() {
			next(w, r)
			return
		}
		ai := authInfoFromContext(r.Context())
		if !ai.IsLoggedIn {
			loginURL := "/auth/login?redirect=" + url.QueryEscape(r.URL.RequestURI())
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		if !ai.IsAdmin {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// handleLogin initiates the OIDC login flow.
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.enabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	state, err := randomString(16)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	verifier, err := randomString(32)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	redirectURL := r.URL.Query().Get("redirect")
	if !isSafeRedirect(redirectURL) {
		redirectURL = "/"
	}

	s.authPending.store(state, pendingAuth{
		Verifier:    verifier,
		RedirectURL: redirectURL,
		ExpiresAt:   time.Now().Add(10 * time.Minute),
	})

	challenge := s256Challenge(verifier)
	cfg := s.auth.oauth2Config()
	authURL := cfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)

	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleCallback handles the OIDC callback.
func (s *server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if !s.auth.enabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	pa, ok := s.authPending.consume(state)
	if !ok {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}

	cfg := s.auth.oauth2Config()
	token, err := cfg.Exchange(r.Context(), code,
		oauth2.SetAuthURLParam("code_verifier", pa.Verifier),
	)
	if err != nil {
		s.logger.Error("token exchange failed", "error", err)
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}

	// Fetch userinfo
	client := cfg.Client(r.Context(), token)
	resp, err := client.Get(s.auth.IssuerURL + "/api/oidc/userinfo")
	if err != nil {
		s.logger.Error("userinfo request failed", "error", err)
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	var userInfo oidcUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		s.logger.Error("userinfo decode failed", "error", err)
		http.Error(w, "authentication failed", http.StatusInternalServerError)
		return
	}

	sessID, err := s.sessions.create(session{
		Email:     userInfo.Email,
		Name:      userInfo.Name,
		Groups:    userInfo.Groups,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "recipes-session",
		Value:    sessID,
		Path:     "/",
		MaxAge:   7 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   s.auth.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})

	s.logger.Info("user logged in", "email", userInfo.Email, "groups", userInfo.Groups)
	http.Redirect(w, r, pa.RedirectURL, http.StatusSeeOther)
}

// handleLogout clears the session and redirects to home.
func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("recipes-session"); err == nil {
		s.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "recipes-session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.auth.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// helpers

func randomString(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func s256Challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// isSafeRedirect returns true if the URL is a relative path safe for post-login redirect.
// It rejects empty strings, absolute URLs, protocol-relative URLs, and other schemes.
func isSafeRedirect(u string) bool {
	if u == "" {
		return false
	}
	// Must start with exactly one slash (reject "//evil.com" and absolute URLs)
	if !strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//") {
		return false
	}
	// Reject backslash tricks ("\/evil.com" is treated as absolute by some browsers)
	if strings.Contains(u, "\\") {
		return false
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	// After parsing, must have no scheme and no host
	return parsed.Scheme == "" && parsed.Host == ""
}


