// Package store is spur's Postgres layer.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

var (
	ErrNotFound  = errors.New("not found")
	ErrSlugTaken = errors.New("slug already taken")
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// migrateLockID guards migrations so several API replicas booting at once don't race.
const migrateLockID = 7_356_001

// Migrate applies every embedded migration that hasn't run yet, in filename order.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockID)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}

	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

type Link struct {
	ID        int64      `json:"-"`
	Slug      string     `json:"slug"`
	URL       string     `json:"url"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (s *Store) CreateLink(ctx context.Context, slug, url string, expiresAt *time.Time) (Link, error) {
	var l Link
	err := s.pool.QueryRow(ctx, `
		INSERT INTO links (slug, url, expires_at) VALUES ($1, $2, $3)
		RETURNING id, slug, url, created_at, expires_at`,
		slug, url, expiresAt,
	).Scan(&l.ID, &l.Slug, &l.URL, &l.CreatedAt, &l.ExpiresAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Link{}, ErrSlugTaken
	}
	return l, err
}

// LinkBySlug returns a live (unexpired) link.
func (s *Store) LinkBySlug(ctx context.Context, slug string) (Link, error) {
	var l Link
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, url, created_at, expires_at FROM links
		WHERE slug = $1 AND (expires_at IS NULL OR expires_at > now())`,
		slug,
	).Scan(&l.ID, &l.Slug, &l.URL, &l.CreatedAt, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	return l, err
}

type Click struct {
	LinkID    int64
	ClickedAt time.Time
	Referrer  string
	Device    string
	IPHash    string
}

// InsertClicks writes a batch of clicks, silently dropping any whose link was deleted meanwhile.
func (s *Store) InsertClicks(ctx context.Context, clicks []Click) error {
	n := len(clicks)
	ids, ats := make([]int64, n), make([]time.Time, n)
	refs, devices, hashes := make([]string, n), make([]string, n), make([]string, n)
	for i, c := range clicks {
		ids[i], ats[i], refs[i], devices[i], hashes[i] = c.LinkID, c.ClickedAt, c.Referrer, c.Device, c.IPHash
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO clicks (link_id, clicked_at, referrer, device, ip_hash)
		SELECT u.* FROM unnest($1::bigint[], $2::timestamptz[], $3::text[], $4::text[], $5::text[])
			AS u(link_id, clicked_at, referrer, device, ip_hash)
		WHERE EXISTS (SELECT 1 FROM links l WHERE l.id = u.link_id)`,
		ids, ats, refs, devices, hashes)
	return err
}

type DayStats struct {
	Day            time.Time `json:"day"`
	Clicks         int64     `json:"clicks"`
	UniqueVisitors int64     `json:"unique_visitors"`
	BotClicks      int64     `json:"bot_clicks"`
}

// DailyStats merges rolled-up days with raw clicks from after the last rollup, newest first.
func (s *Store) DailyStats(ctx context.Context, linkID int64, days int) ([]DayStats, error) {
	rows, err := s.pool.Query(ctx, `
		WITH rolled AS (
			SELECT day, clicks, unique_visitors, bot_clicks FROM daily_stats WHERE link_id = $1
		), raw AS (
			SELECT (clicked_at AT TIME ZONE 'UTC')::date AS day,
			       count(*), count(DISTINCT ip_hash), count(*) FILTER (WHERE device = 'bot')
			FROM clicks
			WHERE link_id = $1
			  AND clicked_at >= COALESCE(
			      (SELECT (max(day) + 1)::timestamp AT TIME ZONE 'UTC' FROM rolled), '-infinity')
			GROUP BY 1
		)
		SELECT * FROM rolled UNION ALL SELECT * FROM raw
		ORDER BY day DESC LIMIT $2`,
		linkID, days)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DayStats, error) {
		var d DayStats
		err := r.Scan(&d.Day, &d.Clicks, &d.UniqueVisitors, &d.BotClicks)
		return d, err
	})
}

// Rollup (re)computes daily_stats for clicks in [from, to). It is idempotent.
func (s *Store) Rollup(ctx context.Context, from, to time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO daily_stats (link_id, day, clicks, unique_visitors, bot_clicks)
		SELECT link_id, (clicked_at AT TIME ZONE 'UTC')::date,
		       count(*), count(DISTINCT ip_hash), count(*) FILTER (WHERE device = 'bot')
		FROM clicks
		WHERE clicked_at >= $1 AND clicked_at < $2
		GROUP BY 1, 2
		ON CONFLICT (link_id, day) DO UPDATE SET
			clicks = EXCLUDED.clicks,
			unique_visitors = EXCLUDED.unique_visitors,
			bot_clicks = EXCLUDED.bot_clicks`,
		from, to)
	return tag.RowsAffected(), err
}

// PurgeExpiredLinks deletes expired links; their clicks and stats cascade.
func (s *Store) PurgeExpiredLinks(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM links WHERE expires_at < now()`)
	return tag.RowsAffected(), err
}

func (s *Store) PurgeClicksBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM clicks WHERE clicked_at < $1`, t)
	return tag.RowsAffected(), err
}
