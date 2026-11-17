# newtonD

newtonD records typed, parameterized logs over UDP. The statement text is the metric name. Search recent events by statement, service or request ID. Query older examples and daily occurrence counts through the web UI or read-only JSON API.

## Gravitas pilot

The public UI is at https://newtond.gravitas.cool. Sign in with Gravitas. In the UI, **Create 30-minute agent token** issues a newtonD read-only bearer token. An agent uses it in the `Authorization` header. The API revalidates the originating IAM session on every request, including agent queries. Keep the token out of logs and URLs.

```text
GET /api/v1/query?kind=events&service=river&request_id=<id>
GET /api/v1/query?kind=samples&service=river&from=2026-11-01
GET /api/v1/query?kind=counts&service=iam&message=Request&from=2026-11-01&to=2026-12-01
```

The API supports `service`, a case-insensitive statement substring in `message`, and an exclusive `to`. Use UTC dates for daily counts. The default result limit is 100 and the maximum is 500. A request timeline is ordered by event time. `/healthz` is anonymous and returns database readiness only.

## Ingestion and retention

The Go `log.NewUDPSender` sends bounded, best-effort datagrams. Version 1 uses a lower-case hex HMAC-SHA256, newline, and one JSON event. Each service has its own key. The collector accepts signed scalar parameters and stores their types separately from their values. IDs are deduplicated before counts change. Only operational metadata approved for pilot users should be emitted. Do not log credentials, query strings, request bodies or personal data.

Full events are retained for at least an hour. Transitions use complete UTC buckets, so a newer tier can briefly cover additional time. The collector keeps one example per statement in hourly, daily and weekly buckets through approximately 24 hours, one month and one year. Hourly and daily samples include finer count bins. The permanent daily counts supply weekly breakdowns. Counts describe events received and deduplicated before expiry. UDP can drop packets.

## Run the receiver

Run `go run . server` with the settings below. The previous terminal demo is available via `go run . tui`.

| Setting | Meaning |
| --- | --- |
| `NEWTOND_UDP_KEYS` | JSON object mapping sending services to distinct keys of at least 32 bytes |
| `NEWTOND_SESSION_KEY` | Unpadded base64url encoding of a 32-byte encryption key |
| `NEWTOND_PUBLIC_ORIGIN` | External HTTPS origin, without a trailing slash |
| `NEWTOND_ISSUER` | Trusted IAM HTTPS origin, defaults to `https://gravitas.cool` |
| `NEWTOND_DB` | SQLite database path, defaults to `./newtond.db` |
| `NEWTOND_UDP` | UDP bind address or multicast group, defaults to `:5514` |
| `NEWTOND_HTTP` | HTTP bind address behind HTTPS termination, defaults to `:8080` |

The Gravitas deployment uses a retained PVC, immutable GHCR image, and private UDP Service. The UDP network policy admits only the River and IAM workloads. The public ingress routes the query UI and API over HTTPS. Run `go test ./...` to verify retention, typed reconstruction, authentication and revocation.

newtonD started at a household hackathon in January 2025 and is named for Newton, the labradoodle.
