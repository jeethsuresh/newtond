package pilot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeethsuresh/newtond/log"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestUDPRoundTripAndRetention(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	store := newStore(t)
	key := []byte(strings.Repeat("a", 32))
	keys := map[string][]byte{"river": key}
	now := time.Now().UTC()
	packet, err := log.EncodeEvent("river", "info", log.MsgFormat{Msg: "Request finished", Timestamp: now,
		Params: map[string]any{"duration": 842 * time.Millisecond, "request_id": "r1", "ok": true}}, key)
	if err != nil {
		t.Fatal(err)
	}
	event, ok := decode(packet, keys)
	if !ok || event.Params["duration"].Type != "duration_ns" {
		t.Fatal("signed typed event was not decoded")
	}
	bad := append([]byte(nil), packet...)
	bad[0] ^= 1
	if _, ok := decode(bad, keys); ok {
		t.Fatal("tampered event was accepted")
	}
	if err := store.Ingest(ctx, []log.Event{event, event}, now); err != nil {
		t.Fatal(err)
	}
	query := Query{Kind: "events", From: now.Add(-time.Minute), To: now.Add(time.Minute), RequestID: "r1", Limit: 100}
	rows, err := store.Query(ctx, query, now)
	if err != nil || len(rows) != 1 || rows[0].Event.Params["duration"].Value != "842000000" {
		t.Fatalf("typed round trip failed: %v, %v", rows, err)
	}
	query = Query{Kind: "samples", From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 100}
	rows, err = store.Query(ctx, query, now.Add(3*time.Hour))
	if err != nil || len(rows) != 1 || rows[0].Count != 1 || len(rows[0].Bins) != 12 {
		t.Fatalf("hourly summary failed: %v, %v", rows, err)
	}
	future := now.AddDate(2, 0, 0)
	if err := store.Prune(ctx, future); err != nil {
		t.Fatal(err)
	}
	query.Kind = "events"
	rows, err = store.Query(ctx, query, future)
	if err != nil || len(rows) != 0 {
		t.Fatalf("expired events returned: %v, %v", rows, err)
	}
	query.Kind = "counts"
	rows, err = store.Query(ctx, query, future)
	if err != nil || len(rows) != 1 || rows[0].Count != 1 {
		t.Fatalf("permanent count lost or duplicated: %v, %v", rows, err)
	}
}

func TestGravitasLoginAndRevokedAgent(t *testing.T) {
	const origin = "https://newtond.example"
	const id = "10000000-0000-0000-0000-000000000000"
	proof := strings.Repeat("a", 43)
	var issuer string
	var expectedChallenge string
	var revoked atomic.Bool
	iam := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			err := r.ParseForm()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if err != nil || r.Form.Get("code") != "test-code" ||
				r.Form.Get("redirect_uri") != origin+callbackPath ||
				base64.RawURLEncoding.EncodeToString(hash[:]) != expectedChallenge {
				t.Error("invalid PKCE exchange")
				w.WriteHeader(400)
				return
			}
			sendJSON(w, map[string]string{"session_check_token": proof, "session_id": id})
		case "/oauth/session":
			if revoked.Load() || r.Header.Get("Authorization") != "Bearer "+proof {
				w.WriteHeader(401)
				return
			}
			sendJSON(w, map[string]any{"active": true, "iss": issuer, "app_origin": origin, "sub": "user",
				"session_id": id, "exp": time.Now().Add(time.Hour).Unix()})
		default:
			w.WriteHeader(404)
		}
	}))
	defer iam.Close()
	issuer = iam.URL
	auth, err := NewAuth(origin, issuer, base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	auth.client = iam.Client()
	handler := Handler(newStore(t), auth)
	call := func(r *http.Request, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: status %d, want %d: %s", r.URL.Path, w.Code, status, w.Body.String())
		}
		return w
	}
	call(httptest.NewRequest("GET", "/api/v1/query", nil), 401)
	landing := call(httptest.NewRequest("GET", "/", nil), 200)
	if landing.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("query page must support browser form Origin validation")
	}
	start := call(httptest.NewRequest("GET", "/auth/gravitas/start", nil), 303)
	location, err := url.Parse(start.Header().Get("Location"))
	if err != nil || len(start.Result().Cookies()) != 1 {
		t.Fatal("invalid sign-in redirect")
	}
	expectedChallenge = location.Query().Get("codeChallenge")
	r := httptest.NewRequest("GET", callbackPath+"?code=test-code&state="+location.Query().Get("state"), nil)
	r.AddCookie(start.Result().Cookies()[0])
	done := call(r, 303)
	var cookie *http.Cookie
	for _, candidate := range done.Result().Cookies() {
		if candidate.Name == sessionCookie {
			cookie = candidate
		}
	}
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly {
		t.Fatal("missing secure session cookie")
	}
	r = httptest.NewRequest("POST", "/api/v1/token", nil)
	r.AddCookie(cookie)
	call(r, 403)
	r.Header.Set("Origin", origin)
	result := call(r, 200)
	var token struct {
		Access string `json:"access_token"`
	}
	if json.Unmarshal(result.Body.Bytes(), &token) != nil || token.Access == "" {
		t.Fatal("missing agent token")
	}
	r = httptest.NewRequest("GET", "/api/v1/query", nil)
	r.Header.Set("Authorization", "Bearer "+token.Access)
	call(r, 200)
	revoked.Store(true)
	call(r, 401)
}
