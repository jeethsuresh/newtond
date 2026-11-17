package pilot

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	flowCookie    = "__Host-newtond_flow"
	sessionCookie = "__Host-newtond_session"
	callbackPath  = "/auth/gravitas/callback"
)

var errUnauthenticated = errors.New("sign in with Gravitas")

type Auth struct {
	origin string
	issuer string
	sealer cipher.AEAD
	client *http.Client
}

type flow struct {
	State, Verifier string
	Expires         int64
}

type session struct {
	Proof, ID, Subject string
	Expires            int64
}

func NewAuth(origin, issuer, key string) (*Auth, error) {
	for _, raw := range []string{origin, issuer} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
			u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return nil, errors.New("auth requires HTTPS origins")
		}
	}
	keyBytes, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(keyBytes) != 32 {
		return nil, errors.New("NEWTOND_SESSION_KEY must be a base64url-encoded 32-byte key")
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}
	sealer, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Auth{origin: origin, issuer: issuer, sealer: sealer, client: &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

func (a *Auth) seal(kind string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.sealer.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(a.sealer.Seal(nonce, nonce, data, []byte(kind))), nil
}

func (a *Auth) unseal(kind, raw string, result any) error {
	if len(raw) > 2048 {
		return errUnauthenticated
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	n := a.sealer.NonceSize()
	if err != nil || len(b) < n {
		return errUnauthenticated
	}
	b, err = a.sealer.Open(nil, b[:n], b[n:], []byte(kind))
	if err != nil || json.Unmarshal(b, result) != nil {
		return errUnauthenticated
	}
	return nil
}

func setCookie(w http.ResponseWriter, name, value string, seconds int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: seconds,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (a *Auth) start(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken()
	if err != nil {
		http.Error(w, "Could not start sign-in", http.StatusServiceUnavailable)
		return
	}
	verifier, err := randomToken()
	if err != nil {
		http.Error(w, "Could not start sign-in", http.StatusServiceUnavailable)
		return
	}
	cookie, err := a.seal("flow", flow{State: state, Verifier: verifier, Expires: time.Now().Add(10 * time.Minute).Unix()})
	if err != nil {
		http.Error(w, "Could not start sign-in", http.StatusServiceUnavailable)
		return
	}
	setCookie(w, flowCookie, cookie, 600)
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{"callerURL": {a.origin}, "redirectURL": {a.origin + callbackPath},
		"codeChallenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "codeChallengeMethod": {"S256"},
		"scope": {"profile.name"}, "state": {state}}
	http.Redirect(w, r, a.issuer+"/oauth/start?"+query.Encode(), http.StatusSeeOther)
}

func (a *Auth) callback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(flowCookie)
	setCookie(w, flowCookie, "", -1)
	var f flow
	q := r.URL.Query()
	if err != nil || a.unseal("flow", cookie.Value, &f) != nil || f.Expires <= time.Now().Unix() ||
		subtle.ConstantTimeCompare([]byte(f.State), []byte(q.Get("state"))) != 1 ||
		q.Get("error") != "" || q.Get("code") == "" {
		http.Error(w, "Sign-in expired or declined. Return to newtonD to try again.", http.StatusUnauthorized)
		return
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")},
		"code_verifier": {f.Verifier}, "redirect_uri": {a.origin + callbackPath}}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.issuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		http.Error(w, "Gravitas sign-in unavailable", http.StatusServiceUnavailable)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		Proof     string `json:"session_check_token"`
		SessionID string `json:"session_id"`
	}
	if err := a.request(req, &token); err != nil || len(token.Proof) != 43 || len(token.SessionID) != 36 {
		http.Error(w, "Could not complete Gravitas sign-in", http.StatusUnauthorized)
		return
	}
	s := session{Proof: token.Proof, ID: token.SessionID, Expires: time.Now().Add(8 * time.Hour).Unix()}
	subject, err := a.validate(r.Context(), s)
	if err != nil {
		authError(w, err)
		return
	}
	s.Subject = subject
	value, err := a.seal("session", s)
	if err != nil {
		http.Error(w, "Could not establish session", http.StatusServiceUnavailable)
		return
	}
	setCookie(w, sessionCookie, value, 8*3600)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *Auth) request(req *http.Request, output any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return errUnauthenticated
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("IAM returned status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(output)
}

func (a *Auth) validate(ctx context.Context, s session) (string, error) {
	if s.Expires <= time.Now().Unix() || len(s.Proof) != 43 || len(s.ID) != 36 {
		return "", errUnauthenticated
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.issuer+"/oauth/session", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.Proof)
	var status struct {
		Active  bool   `json:"active"`
		Issuer  string `json:"iss"`
		Origin  string `json:"app_origin"`
		Subject string `json:"sub"`
		ID      string `json:"session_id"`
		Expires int64  `json:"exp"`
	}
	if err := a.request(req, &status); err != nil {
		return "", err
	}
	if !status.Active || status.Issuer != a.issuer || status.Origin != a.origin ||
		status.ID != s.ID || status.Subject == "" || (s.Subject != "" && status.Subject != s.Subject) ||
		status.Expires <= time.Now().Unix() {
		return "", errUnauthenticated
	}
	return status.Subject, nil
}

func (a *Auth) authorize(r *http.Request, allowBearer bool) (session, error) {
	var s session
	kind, token := "session", ""
	if raw := r.Header.Get("Authorization"); allowBearer && raw != "" {
		prefix, value, ok := strings.Cut(raw, " ")
		if !ok || !strings.EqualFold(prefix, "Bearer") {
			return s, errUnauthenticated
		}
		kind, token = "agent", strings.TrimSpace(value)
	} else if cookie, err := r.Cookie(sessionCookie); err == nil {
		token = cookie.Value
	}
	if err := a.unseal(kind, token, &s); err != nil {
		return s, err
	}
	_, err := a.validate(r.Context(), s)
	return s, err
}

func authError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUnauthenticated) {
		http.Error(w, "Sign in with Gravitas.", http.StatusUnauthorized)
		return
	}
	http.Error(w, "Gravitas session verification is unavailable.", http.StatusServiceUnavailable)
}

func (a *Auth) agentToken(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != a.origin {
		http.Error(w, "Origin must match newtonD", http.StatusForbidden)
		return
	}
	s, err := a.authorize(r, false)
	if err != nil {
		authError(w, err)
		return
	}
	s.Expires = min(s.Expires, time.Now().Add(30*time.Minute).Unix())
	token, err := a.seal("agent", s)
	if err != nil {
		http.Error(w, "Token creation failed", http.StatusServiceUnavailable)
		return
	}
	sendJSON(w, map[string]any{"access_token": token, "token_type": "Bearer", "expires_at": s.Expires})
}
