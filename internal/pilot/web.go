package pilot

import (
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func Handler(store *Store, auth *Auth) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := store.db.PingContext(r.Context()); err != nil {
			http.Error(w, "Database unavailable", http.StatusServiceUnavailable)
			return
		}
		sendJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /auth/gravitas/start", auth.start)
	mux.HandleFunc("GET "+callbackPath, auth.callback)
	mux.HandleFunc("POST /api/v1/token", auth.agentToken)
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != auth.origin {
			http.Error(w, "Origin must match newtonD", http.StatusForbidden)
			return
		}
		setCookie(w, sessionCookie, "", -1)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		if _, err := auth.authorize(r, true); err != nil {
			authError(w, err)
			return
		}
		now := time.Now().UTC()
		query, err := parseQuery(r.URL.Query(), now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		results, err := store.Query(r.Context(), query, now)
		if err != nil {
			http.Error(w, "Query failed", http.StatusServiceUnavailable)
			return
		}
		sendJSON(w, map[string]any{"kind": query.Kind, "results": results,
			"from": query.From, "to": query.To, "limit": query.Limit})
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The query page has same-origin POST forms. A no-referrer document
		// can cause browsers to serialize the form's Origin as null.
		w.Header().Set("Referrer-Policy", "same-origin")
		if _, err := auth.authorize(r, false); err != nil {
			if errors.Is(err, errUnauthenticated) {
				_ = page.Execute(w, pageData{})
			} else {
				authError(w, err)
			}
			return
		}
		data := pageData{SignedIn: true, Filters: r.URL.Query()}
		now := time.Now().UTC()
		data.Query, _ = parseQuery(nil, now)
		query, err := parseQuery(r.URL.Query(), now)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Query = query
			if records, err := store.Query(r.Context(), query, now); err != nil {
				data.Error = "Query failed."
			} else {
				data.Results = records
			}
		}
		_ = page.Execute(w, data)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})
}

func sendJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func parseQuery(values url.Values, now time.Time) (Query, error) {
	q := Query{Kind: values.Get("kind"), Service: values.Get("service"), Message: values.Get("message"),
		RequestID: values.Get("request_id"), To: now.Add(time.Minute), Limit: 100}
	switch q.Kind {
	case "", "events":
		q.Kind = "events"
		q.From = time.Unix(rawCutoff(now), 0).UTC()
	case "counts":
		q.From = time.Unix(floor(now.Unix(), day), 0).UTC().AddDate(0, 0, -29)
	case "samples":
		q.From = now.AddDate(0, 0, -7)
	default:
		return q, errors.New("kind must be events, samples, or counts")
	}
	if (q.Service != "" && !serviceName.MatchString(q.Service)) || len(q.Message) > 180 || len(q.RequestID) > 128 {
		return q, errors.New("invalid service, message, or request ID")
	}
	if q.RequestID != "" && q.Kind != "events" {
		return q, errors.New("request ID search is available for full events")
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 500 {
			return q, errors.New("limit must be between 1 and 500")
		}
		q.Limit = limit
	}
	for name, target := range map[string]*time.Time{"from": &q.From, "to": &q.To} {
		if raw := values.Get(name); raw != "" {
			format := time.RFC3339
			if len(raw) == 10 {
				format = time.DateOnly
			}
			value, err := time.Parse(format, raw)
			if err != nil || value.Year() < 1971 || value.Year() > 2200 {
				return q, errors.New("dates must be YYYY-MM-DD or RFC3339")
			}
			if q.Kind == "counts" && value.Unix()%day != 0 {
				return q, errors.New("daily counts use UTC dates")
			}
			*target = value.UTC()
		}
	}
	if !q.From.Before(q.To) {
		return q, errors.New("from must be before to")
	}
	return q, nil
}

type pageData struct {
	SignedIn bool
	Error    string
	Query    Query
	Filters  url.Values
	Results  []Record
}

func (d pageData) Kind() string {
	if kind := d.Filters.Get("kind"); kind != "" {
		return kind
	}
	return d.Query.Kind
}

//go:embed query.html
var queryPageHTML string

var page = template.Must(template.New("newtond").Funcs(template.FuncMap{
	"json": func(data any) string {
		b, _ := json.MarshalIndent(data, "", "  ")
		return string(b)
	},
}).Parse(queryPageHTML))
