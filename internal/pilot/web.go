package pilot

import (
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
		if _, err := auth.authorize(r, false); err != nil {
			if errors.Is(err, errUnauthenticated) {
				_ = page.Execute(w, pageData{})
			} else {
				authError(w, err)
			}
			return
		}
		data := pageData{SignedIn: true}
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
	Results  []Record
}

var page = template.Must(template.New("newtond").Funcs(template.FuncMap{
	"json": func(data any) string {
		b, _ := json.MarshalIndent(data, "", "  ")
		return string(b)
	},
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>newtonD · Gravitas</title>
<style>
:root{font-family:"Instrument Sans",system-ui,sans-serif;color:#172923;background:#f5f6f0;line-height:1.45}
body{margin:0}main{max-width:68rem;padding:clamp(1rem,4vw,3rem);margin:auto}
header{border-bottom:1px solid #cbd2c7;padding-bottom:1rem;margin-bottom:1.25rem}
h1{margin:0;font-size:2.6rem;letter-spacing:-.05em}
p{max-width:70ch}a{color:#15563f}label{display:grid;gap:.25rem;font-size:.9rem}
.query{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,13rem),1fr));gap:.7rem;align-items:end}
input,select,button{padding:.45rem .55rem;border:1px solid #aabbb0;border-radius:4px;background:white;font:inherit;min-width:0}
button{background:#164b39;color:white;cursor:pointer}
.actions{display:flex;gap:.6rem;flex-wrap:wrap;margin-top:1rem}
article{background:white;border:1px solid #cbd2c7;padding:.9rem;margin:.6rem 0;overflow-wrap:anywhere}
summary{cursor:pointer}pre{overflow:auto;font-size:.85rem}small{color:#405a4d}footer{margin-top:1.2rem}
</style></head><body><main><header><small>Gravitas</small><h1>newtonD</h1></header>
{{if .SignedIn}}
<form method="get" class="query">
<label>View<select name="kind"><option value="events" {{if eq .Query.Kind "events"}}selected{{end}}>Recent events</option><option value="samples" {{if eq .Query.Kind "samples"}}selected{{end}}>Retained examples</option><option value="counts" {{if eq .Query.Kind "counts"}}selected{{end}}>Daily counts</option></select></label>
<label>Service<input name="service" placeholder="river" value="{{.Query.Service}}"></label>
<label>Statement contains<input name="message" value="{{.Query.Message}}"></label>
<label>Request ID<input name="request_id" value="{{.Query.RequestID}}"></label>
<label>From (UTC)<input name="from" placeholder="YYYY-MM-DD"></label>
<label>To (UTC, exclusive)<input name="to" placeholder="YYYY-MM-DD"></label>
<button>Query logs</button></form>
<div class="actions"><form method="post" action="/api/v1/token"><button>Create 30-minute agent token</button></form>
<form method="post" action="/auth/logout"><button>Sign out</button></form></div>
<p><small>Showing up to {{.Query.Limit}} rows. Counts include events received by newtonD. Older buckets contain one example.</small></p>
{{if .Error}}<p role="alert">{{.Error}}</p>{{else}}
{{range .Results}}<article><strong>{{.Message}}</strong> <small>{{.Service}}</small>
{{if .Bucket}}<p>UTC bucket {{.Bucket}} · {{.Count}} occurrences · {{.Seconds}} seconds</p>{{end}}
{{if .Bins}}<small>Counts within bucket: {{.Bins}}</small>{{end}}
{{if .Event}}<details><summary>{{.Event.Timestamp.Format "2006-01-02 15:04:05.000Z"}} · {{.Event.Level}}</summary><pre>{{json .Event.Params}}</pre><small>Event {{.Event.ID}}</small></details>{{end}}
</article>{{else}}<p>No matching logs.</p>{{end}}{{end}}
{{else}}<p>Sign in to query Gravitas and River logs.</p><a href="/auth/gravitas/start">Sign in with Gravitas</a>{{end}}
</main></body></html>`))
