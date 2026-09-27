// Command cron is a run-once maintenance job: roll up stats, then prune old data.
// Railway cron services must exit when done, so this does not loop.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/gitmobkab/spur/internal/config"
	"github.com/gitmobkab/spur/internal/store"
)

// rollupDays re-aggregates this many past days each run, catching clicks the worker delivered late.
const rollupDays = 2

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("cron failed", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	if err := config.Require("DATABASE_URL"); err != nil {
		return err
	}
	cfg := config.Load()
	// Raw clicks must outlive the rollup window or they'd vanish before being aggregated.
	retention := max(cfg.RetentionDays, rollupDays+1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	today := time.Now().UTC().Truncate(24 * time.Hour)

	rolled, err := st.Rollup(ctx, today.AddDate(0, 0, -rollupDays), today)
	if err != nil {
		return err
	}
	expired, err := st.PurgeExpiredLinks(ctx)
	if err != nil {
		return err
	}
	pruned, err := st.PurgeClicksBefore(ctx, today.AddDate(0, 0, -retention))
	if err != nil {
		return err
	}

	log.Info("cron done", "rolled_up_rows", rolled, "expired_links", expired, "pruned_clicks", pruned)
	return nil
}
