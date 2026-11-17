package pilot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jeethsuresh/newtond/log"
	_ "modernc.org/sqlite"
)

const (
	hour = int64(3600)
	day  = hour * 24
	week = day * 7
)

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS statements (
  id INTEGER PRIMARY KEY, service TEXT NOT NULL, message TEXT NOT NULL,
  UNIQUE(service, message)
);
CREATE TABLE IF NOT EXISTS layouts (
  id INTEGER PRIMARY KEY, statement_id INTEGER NOT NULL REFERENCES statements(id),
  fields TEXT NOT NULL, UNIQUE(statement_id, fields)
);
CREATE TABLE IF NOT EXISTS events (
  id TEXT PRIMARY KEY, timestamp INTEGER NOT NULL, layout_id INTEGER NOT NULL REFERENCES layouts(id),
  request_id TEXT NOT NULL, level TEXT NOT NULL, values_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_time ON events(timestamp);
CREATE INDEX IF NOT EXISTS events_request ON events(request_id, timestamp) WHERE request_id != '';
CREATE TABLE IF NOT EXISTS daily_counts (
  statement_id INTEGER NOT NULL REFERENCES statements(id), day INTEGER NOT NULL,
  count INTEGER NOT NULL, PRIMARY KEY(statement_id, day)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS daily_counts_day ON daily_counts(day);
CREATE TABLE IF NOT EXISTS summaries (
  period INTEGER NOT NULL, bucket INTEGER NOT NULL,
  statement_id INTEGER NOT NULL REFERENCES statements(id), bins TEXT NOT NULL, count INTEGER NOT NULL,
  rank TEXT NOT NULL, sample_id TEXT NOT NULL, sample_timestamp INTEGER NOT NULL,
  layout_id INTEGER NOT NULL REFERENCES layouts(id), request_id TEXT NOT NULL,
  level TEXT NOT NULL, values_json TEXT NOT NULL,
  PRIMARY KEY(period, bucket, statement_id)
) WITHOUT ROWID;
`

func Open(path string) (*Store, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	for _, pragma := range []string{"journal_mode(WAL)", "synchronous(FULL)", "foreign_keys(1)", "busy_timeout(3000)"} {
		q.Add("_pragma", pragma)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type field struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s *Store) Ingest(ctx context.Context, events []log.Event, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, event := range events {
		if event.Timestamp.Unix() < rawCutoff(now) || event.Timestamp.After(now.Add(time.Minute)) {
			continue
		}
		if err := insert(ctx, tx, event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insert(ctx context.Context, tx *sql.Tx, event log.Event) error {
	names := make([]string, 0, len(event.Params))
	for name := range event.Params {
		names = append(names, name)
	}
	slices.Sort(names)
	fields := make([]field, 0, len(names))
	values := make([]string, 0, len(names))
	for _, name := range names {
		value := event.Params[name]
		fields = append(fields, field{name, value.Type})
		values = append(values, value.Value)
	}
	layout, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return err
	}
	var statementID, layoutID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO statements(service,message) VALUES (?,?)
		ON CONFLICT(service,message) DO UPDATE SET message=excluded.message RETURNING id`,
		event.Service, event.Message).Scan(&statementID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `INSERT INTO layouts(statement_id,fields) VALUES (?,?)
		ON CONFLICT(statement_id,fields) DO UPDATE SET fields=excluded.fields RETURNING id`,
		statementID, string(layout)).Scan(&layoutID); err != nil {
		return err
	}
	requestID := event.Params["request_id"].Value
	result, err := tx.ExecContext(ctx, `INSERT INTO events VALUES (?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		event.ID, event.Timestamp.UnixNano(), layoutID, requestID, event.Level, string(payload))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 {
		return err
	}
	sec := event.Timestamp.Unix()
	if _, err := tx.ExecContext(ctx, `INSERT INTO daily_counts VALUES (?,?,1)
		ON CONFLICT(statement_id,day) DO UPDATE SET count=count+1`, statementID, floor(sec, day)); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(event.ID))
	rank := hex.EncodeToString(hash[:])
	for _, period := range []int64{hour, day, week} {
		bucket := bucketStart(sec, period)
		size, index := 0, 0
		switch period {
		case hour:
			size, index = 12, int((sec-bucket)/300)
		case day:
			size, index = 24, int((sec-bucket)/hour)
		}
		bins := make([]int, size)
		if size > 0 {
			bins[index] = 1
		}
		binsJSON, err := json.Marshal(bins)
		if err != nil {
			return err
		}
		bin := fmt.Sprintf("$[%d]", index)
		if _, err := tx.ExecContext(ctx, `INSERT INTO summaries VALUES (?,?,?,?,1,?,?,?,?,?,?,?)
			ON CONFLICT(period,bucket,statement_id) DO UPDATE SET
			  count=count+1,
			  bins=CASE WHEN excluded.period=? THEN bins ELSE json_set(bins,?,json_extract(bins,?)+1) END,
			  sample_id=CASE WHEN excluded.rank<rank THEN excluded.sample_id ELSE sample_id END,
			  sample_timestamp=CASE WHEN excluded.rank<rank THEN excluded.sample_timestamp ELSE sample_timestamp END,
			  layout_id=CASE WHEN excluded.rank<rank THEN excluded.layout_id ELSE layout_id END,
			  request_id=CASE WHEN excluded.rank<rank THEN excluded.request_id ELSE request_id END,
			  level=CASE WHEN excluded.rank<rank THEN excluded.level ELSE level END,
			  values_json=CASE WHEN excluded.rank<rank THEN excluded.values_json ELSE values_json END,
			  rank=min(rank,excluded.rank)`,
			period, bucket, statementID, string(binsJSON), rank, event.ID, event.Timestamp.UnixNano(),
			layoutID, requestID, event.Level, string(payload), week, bin, bin); err != nil {
			return err
		}
	}
	return nil
}

func floor(ts, period int64) int64 { return ts - ts%period }

func bucketStart(ts, period int64) int64 {
	if period == week {
		// Unix epoch was a Thursday. Weekly buckets start on Monday.
		return floor(ts+3*day, week) - 3*day
	}
	return floor(ts, period)
}

func rawCutoff(now time.Time) int64 {
	return floor(now.Add(-time.Hour).Unix(), hour)
}

func (s *Store) Prune(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM events WHERE timestamp < ?", rawCutoff(now)*int64(time.Second)); err != nil {
		return err
	}
	for _, tier := range []struct {
		period int64
		cutoff int64
	}{
		{hour, floor(now.Add(-24*time.Hour).Unix(), day)},
		{day, bucketStart(now.UTC().AddDate(0, -1, 0).Unix(), week)},
		{week, bucketStart(now.UTC().AddDate(-1, 0, 0).Unix(), week)},
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM summaries WHERE period=? AND bucket<?", tier.period, tier.cutoff); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type Query struct {
	Kind, Service, Message, RequestID string
	From, To                          time.Time
	Limit                             int
}

type Record struct {
	Service string     `json:"service"`
	Message string     `json:"message"`
	Event   *log.Event `json:"event,omitempty"`
	Bucket  string     `json:"bucket,omitempty"`
	Seconds int64      `json:"bucket_seconds,omitempty"`
	Count   int64      `json:"count,omitempty"`
	Bins    []int64    `json:"bins,omitempty"`
}

func (s *Store) Query(ctx context.Context, q Query, now time.Time) ([]Record, error) {
	var sqlText, order string
	var args []any
	switch q.Kind {
	case "events":
		sqlText = `SELECT t.service,t.message,e.id,e.timestamp,e.level,l.fields,e.values_json,0,0,0,'[]'
			FROM events e JOIN layouts l ON e.layout_id=l.id JOIN statements t ON t.id=l.statement_id
			WHERE e.timestamp>=? AND e.timestamp<? AND e.timestamp>=?`
		args = []any{q.From.UnixNano(), q.To.UnixNano(), rawCutoff(now) * int64(time.Second)}
		order = "e.timestamp DESC,e.id"
		if q.RequestID != "" {
			sqlText += " AND e.request_id=?"
			args = append(args, q.RequestID)
			order = "e.timestamp,e.id"
		}
	case "counts":
		sqlText = `SELECT t.service,t.message,'',0,'','[]','[]',d.day,86400,d.count,'[]'
			FROM daily_counts d JOIN statements t ON t.id=d.statement_id WHERE d.day>=? AND d.day<?`
		args = []any{floor(q.From.Unix(), day), q.To.Unix()}
		order = "d.day DESC,t.service,t.message"
	case "samples":
		sqlText = `SELECT t.service,t.message,a.sample_id,a.sample_timestamp,a.level,l.fields,a.values_json,
			a.bucket,a.period,a.count,a.bins
			FROM summaries a JOIN statements t ON t.id=a.statement_id JOIN layouts l ON l.id=a.layout_id
			WHERE a.bucket+a.period>? AND a.bucket<? AND (
			  (a.period=3600 AND a.bucket>=? AND a.bucket<?) OR
			  (a.period=86400 AND a.bucket>=? AND a.bucket<?) OR
			  (a.period=604800 AND a.bucket>=? AND a.bucket<?))`
		hourStart := floor(now.Add(-24*time.Hour).Unix(), day)
		dayStart := bucketStart(now.UTC().AddDate(0, -1, 0).Unix(), week)
		weekStart := bucketStart(now.UTC().AddDate(-1, 0, 0).Unix(), week)
		args = []any{q.From.Unix(), q.To.Unix(), hourStart, rawCutoff(now),
			dayStart, hourStart, weekStart, dayStart}
		order = "a.bucket DESC,t.service,t.message"
	default:
		return nil, errors.New("unknown query type")
	}
	if q.Service != "" {
		sqlText += " AND t.service=?"
		args = append(args, q.Service)
	}
	if q.Message != "" {
		sqlText += " AND instr(lower(t.message),lower(?))>0"
		args = append(args, q.Message)
	}
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, sqlText+" ORDER BY "+order+" LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Record, 0)
	for rows.Next() {
		var rec Record
		var id, level, types, values, bins string
		var timestamp, bucket int64
		if err := rows.Scan(&rec.Service, &rec.Message, &id, &timestamp, &level, &types, &values,
			&bucket, &rec.Seconds, &rec.Count, &bins); err != nil {
			return nil, err
		}
		if rec.Seconds != 0 {
			rec.Bucket = time.Unix(bucket, 0).UTC().Format(time.RFC3339)
		}
		if err := json.Unmarshal([]byte(bins), &rec.Bins); err != nil {
			return nil, err
		}
		if id != "" {
			var fields []field
			var params []string
			if err := json.Unmarshal([]byte(types), &fields); err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(values), &params); err != nil {
				return nil, err
			}
			if len(fields) != len(params) {
				return nil, errors.New("event layout differs from values")
			}
			e := log.Event{Version: 1, ID: id, Service: rec.Service, Message: rec.Message,
				Timestamp: time.Unix(0, timestamp).UTC(), Level: strings.ToLower(level), Params: make(map[string]log.Value, len(fields))}
			for i, f := range fields {
				e.Params[f.Name] = log.Value{Type: f.Type, Value: params[i]}
			}
			rec.Event = &e
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
