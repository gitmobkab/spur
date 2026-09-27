// Package api is spur's HTTP layer: redirects, link creation and stats.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gitmobkab/spur/internal/cache"
	"github.com/gitmobkab/spur/internal/config"
	"github.com/gitmobkab/spur/internal/events"
	"github.com/gitmobkab/spur/internal/store"
)

const (
	maxCacheTTL      = time.Hour
	negativeCacheTTL = time.Minute
	maxTTLHours      = 24 * 365
	authFailLimit    = 10
)

type Server struct {
	Store *store.Store
	Redis *redis.Client
	Cfg   config.Config
	Log   *slog.Logger
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /{slug}", s.redirect)
	mux.Handle("POST /api/links", s.requireToken(http.HandlerFunc(s.createLink)))
	mux.Handle("GET /api/links/{slug}/stats", s.requireToken(http.HandlerFunc(s.stats)))
	return s.securityHeaders(s.logRequests(mux))
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("spur - a tiny link shortener\n"))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status := map[string]string{"postgres": "ok", "redis": "ok"}
	code := http.StatusOK
	if err := s.Store.Ping(ctx); err != nil {
		status["postgres"], code = "down", http.StatusServiceUnavailable
	}
	if err := s.Redis.Ping(ctx).Err(); err != nil {
		status["redis"], code = "down", http.StatusServiceUnavailable
	}
	writeJSON(w, code, status)
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := r.PathValue("slug")
	if !slugPattern.MatchString(slug) {
		http.NotFound(w, r)
		return
	}

	ipHash := s.hashIP(s.clientIP(r))
	if ok, err := cache.Allow(ctx, s.Redis, "redir:"+ipHash, s.Cfg.RatePerMinute, time.Minute); err != nil {
		s.Log.Warn("rate limiter unavailable", "err", err) // fail open: redirects matter more than limits
	} else if !ok {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	entry, err := s.lookup(ctx, slug)
	if err != nil {
		s.Log.Error("lookup failed", "slug", slug, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if entry.ID == 0 {
		http.NotFound(w, r)
		return
	}

	click := events.Click{
		LinkID:    entry.ID,
		At:        time.Now().UTC(),
		Referrer:  refererHost(r.Referer()),
		UserAgent: r.UserAgent(),
		IPHash:    ipHash,
	}
	pubCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := events.Publish(pubCtx, s.Redis, click); err != nil {
		s.Log.Warn("click not recorded", "slug", slug, "err", err)
	}

	// no-store so every visit reaches spur and gets counted.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, entry.URL, http.StatusFound)
}

// lookup resolves a slug via Redis, falling back to Postgres and caching the result (misses included).
func (s *Server) lookup(ctx context.Context, slug string) (cache.Entry, error) {
	if e, ok, err := cache.GetLink(ctx, s.Redis, slug); err == nil && ok {
		return e, nil
	} else if err != nil {
		s.Log.Warn("cache read failed", "err", err)
	}

	link, err := s.Store.LinkBySlug(ctx, slug)
	if errors.Is(err, store.ErrNotFound) {
		cache.SetLink(ctx, s.Redis, slug, cache.Entry{}, negativeCacheTTL)
		return cache.Entry{}, nil
	}
	if err != nil {
		return cache.Entry{}, err
	}

	ttl := maxCacheTTL
	if link.ExpiresAt != nil {
		ttl = min(ttl, time.Until(*link.ExpiresAt))
	}
	e := cache.Entry{ID: link.ID, URL: link.URL}
	if ttl > 0 {
		cache.SetLink(ctx, s.Redis, slug, e, ttl)
	}
	return e, nil
}

type createRequest struct {
	URL      string `json:"url"`
	Slug     string `json:"slug"`
	TTLHours int    `json:"ttl_hours"`
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req createRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	target, err := validateTarget(req.URL, s.selfHost(r))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if req.Slug != "" && !validSlug(req.Slug) {
		writeError(w, http.StatusUnprocessableEntity, "slug must be 3-32 chars of letters, digits, '-' or '_' and not reserved")
		return
	}
	if req.TTLHours < 0 || req.TTLHours > maxTTLHours {
		writeError(w, http.StatusUnprocessableEntity, "ttl_hours must be between 0 and 8760")
		return
	}
	var expiresAt *time.Time
	if req.TTLHours > 0 {
		t := time.Now().Add(time.Duration(req.TTLHours) * time.Hour).UTC()
		expiresAt = &t
	}

	var link store.Link
	for attempt := 0; ; attempt++ {
		slug := req.Slug
		if slug == "" {
			slug = newSlug()
		}
		link, err = s.Store.CreateLink(r.Context(), slug, target, expiresAt)
		if errors.Is(err, store.ErrSlugTaken) && req.Slug == "" && attempt < 5 {
			continue
		}
		break
	}
	switch {
	case errors.Is(err, store.ErrSlugTaken):
		writeError(w, http.StatusConflict, "slug already taken")
		return
	case err != nil:
		s.Log.Error("create link failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Clear any cached miss for this slug.
	cache.DeleteLink(r.Context(), s.Redis, link.Slug)

	writeJSON(w, http.StatusCreated, map[string]any{
		"slug":       link.Slug,
		"short_url":  s.baseURL(r) + "/" + link.Slug,
		"url":        link.URL,
		"created_at": link.CreatedAt,
		"expires_at": link.ExpiresAt,
	})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	link, err := s.Store.LinkBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "link not found")
		return
	}
	if err != nil {
		s.Log.Error("stats lookup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	days, err := s.Store.DailyStats(r.Context(), link.ID, 30)
	if err != nil {
		s.Log.Error("stats query failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var total int64
	for _, d := range days {
		total += d.Clicks
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"link":            link,
		"clicks_last_30d": total,
		"days":            days,
	})
}

// requireToken guards admin endpoints with a bearer token and throttles repeated failures per IP.
func (s *Server) requireToken(next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(s.Cfg.AdminToken))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failKey := "authfail:" + s.hashIP(s.clientIP(r))
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(token))
		if token != "" && subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if ok, err := cache.Allow(r.Context(), s.Redis, failKey, authFailLimit, time.Minute); err == nil && !ok {
			writeError(w, http.StatusTooManyRequests, "too many failed attempts")
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="spur"`)
		writeError(w, http.StatusUnauthorized, "unauthorized")
	})
}

// clientIP returns the caller's IP. Behind Railway's edge, X-Real-IP / the last
// X-Forwarded-For hop are set by the proxy; elsewhere those headers are spoofable.
func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustProxy {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// hashIP pseudonymises an IP with a daily-rotating salt, so raw IPs are never stored
// and "unique visitors" can't be correlated across days.
func (s *Server) hashIP(ip string) string {
	day := time.Now().UTC().Format(time.DateOnly)
	sum := sha256.Sum256([]byte(s.Cfg.IPHashSalt + "|" + day + "|" + ip))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) baseURL(r *http.Request) string {
	if s.Cfg.BaseURL != "" {
		return s.Cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || (s.Cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) selfHost(r *http.Request) string {
	if u, err := url.Parse(s.baseURL(r)); err == nil {
		return u.Hostname()
	}
	return ""
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if s.Cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.Log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// refererHost keeps only the referring host, dropping paths and query strings that may carry personal data.
func refererHost(ref string) string {
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
