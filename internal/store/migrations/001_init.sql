CREATE TABLE links (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug       text        NOT NULL UNIQUE,
    url        text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz
);
CREATE INDEX links_expires_at_idx ON links (expires_at) WHERE expires_at IS NOT NULL;

-- Raw click events, written by the worker and pruned by the cron job.
CREATE TABLE clicks (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    link_id    bigint      NOT NULL REFERENCES links (id) ON DELETE CASCADE,
    clicked_at timestamptz NOT NULL,
    referrer   text        NOT NULL DEFAULT '',
    device     text        NOT NULL,
    ip_hash    text        NOT NULL
);
CREATE INDEX clicks_link_time_idx ON clicks (link_id, clicked_at);
CREATE INDEX clicks_time_idx ON clicks (clicked_at);

-- Per-day aggregates, rebuilt by the cron job so raw clicks can expire.
CREATE TABLE daily_stats (
    link_id         bigint NOT NULL REFERENCES links (id) ON DELETE CASCADE,
    day             date   NOT NULL,
    clicks          bigint NOT NULL,
    unique_visitors bigint NOT NULL,
    bot_clicks      bigint NOT NULL,
    PRIMARY KEY (link_id, day)
);
