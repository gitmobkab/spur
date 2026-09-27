# spur

A tiny link shortener with click analytics, built as a playground for testing [Railway](https://railway.com).

```
[ api ]  --public domain-->  users          (Railpack build)
   |  private network (*.railway.internal)
   +--> [ Postgres ]  links, clicks, daily_stats
   +--> [ Redis ]     link cache, rate limits, click queue
[ worker ]  <-- Redis queue -> Postgres       (Railpack build)
[ cron ]    daily: roll up stats, purge old data (Railpack build, cron schedule)
```

## API

| Method | Path | Auth | |
|---|---|---|---|
| `GET` | `/{slug}` | none | 302 to the target, records a click |
| `POST` | `/api/links` | Bearer | `{"url": "...", "slug": "optional", "ttl_hours": 0}` |
| `GET` | `/api/links/{slug}/stats` | Bearer | last 30 days of clicks |
| `GET` | `/healthz` | none | Postgres + Redis status |

```sh
curl -X POST https://<host>/api/links -H "Authorization: Bearer $ADMIN_TOKEN" -d '{"url":"https://go.dev","slug":"golang"}'
```

## Local development

```sh
make env        # writes .env with fresh ADMIN_TOKEN / IP_HASH_SALT
make deps       # Postgres + Redis via docker compose
make api        # and in other terminals: make worker / make cron
make test
```

## Deploying to Railway

1. Push this repo to GitHub.
2. New project → add **Postgres** and **Redis** from the database templates.
3. Add three services from the same GitHub repo: `api`, `worker`, `cron`. For each, set the build/start
   commands under **Settings → Build** / **Deploy**:

   | Service | Build command | Start command |
   |---|---|---|
   | `api` | `go build -ldflags='-s -w' -o out ./cmd/api` | `./out` |
   | `worker` | `go build -ldflags='-s -w' -o out ./cmd/worker` | `./out` |
   | `cron` | `go build -ldflags='-s -w' -o out ./cmd/cron` | `./out` |

   Set `cron`'s **Cron Schedule** (e.g. `15 3 * * *`) under Settings, and its restart policy to Never.
4. Variables (use reference variables so secrets aren't copy-pasted):

   | Variable | api | worker | cron |
   |---|---|---|---|
   | `DATABASE_URL` = `${{Postgres.DATABASE_URL}}` | ✓ | ✓ | ✓ |
   | `REDIS_URL` = `${{Redis.REDIS_URL}}` | ✓ | ✓ | |
   | `ADMIN_TOKEN` (`openssl rand -hex 32`, **sealed**) | ✓ | | |
   | `IP_HASH_SALT` (random, **sealed**) | ✓ | | |
   | `BASE_URL` (optional, e.g. `https://${{RAILWAY_PUBLIC_DOMAIN}}`) | ✓ | | |

   `DATABASE_URL` / `REDIS_URL` use the private network hostnames; don't use the `*_PUBLIC_URL` variants.
5. Generate a public domain for `api` only. The worker and cron don't need one.

## Things to try on Railway

**Platform**
- Set each service's **Settings → Watch Paths** (e.g. `cmd/worker/**`, `internal/**`, `go.mod`, `go.sum` for the worker),
  then push a change under `cmd/worker/` and confirm only the worker redeploys.
- Break `/healthz` on purpose and watch the deploy fail its healthcheck and keep the old version live.
- Roll back a deploy from the dashboard.
- Scale `api` to 2+ replicas. Migrations are guarded by an advisory lock, so boots don't race.
- Enable app sleeping (serverless) on the worker and see how the queue behaves.
- Create a `staging` environment and enable PR environments.
- Trigger the cron job manually, then check its logs and exit status.
- Take a Postgres backup and restore it.

**Security**
- Confirm Postgres/Redis have no public TCP proxy. Then enable one and see what's exposed.
- Check whether sealed variables ever appear in build logs, the CLI (`railway variables`), or PR environments.
- Check whether PR environments inherit production variables and data, and whether forks can trigger them.
- Look at team roles, 2FA enforcement and the audit log (who changed which variable, who deployed).
- Verify client IP handling: send a spoofed `X-Forwarded-For` / `X-Real-IP` to the public domain and check
  whether rate limiting can be bypassed. spur trusts these headers only when `RAILWAY_ENVIRONMENT_NAME` is set.
- Send 10+ requests with a bad token → `429` (per-IP auth-failure throttling).

## Built-in safeguards

- Admin token: ≥32 chars, compared in constant time, failures throttled per IP.
- Target URLs: http(s) only, no credentials in URL, no localhost/private/link-local IPs or `*.internal`, no self-redirects.
- Privacy: raw IPs are never stored (salted SHA-256 with a daily rotating salt); only the referrer's host is kept.
- Redis queue is capped at 100k events so a stopped worker can't exhaust memory.
- Security headers (CSP, `X-Frame-Options`, `nosniff`, `Referrer-Policy: no-referrer`, HSTS behind HTTPS).

Known trade-off: the worker pops events with `BRPOP`, so a batch in flight is lost if the process is killed hard
between popping and inserting. `BLMOVE` into a processing list would fix that.
