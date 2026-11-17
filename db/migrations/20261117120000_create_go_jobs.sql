CREATE TABLE go_jobs (
  id          bigserial PRIMARY KEY,
  kind        text        NOT NULL,
  args        jsonb       NOT NULL DEFAULT '{}',
  run_at      timestamptz NOT NULL DEFAULT now(),
  attempts    integer     NOT NULL DEFAULT 0,
  locked_at   timestamptz,
  finished_at timestamptz,
  last_error  text,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX index_go_jobs_runnable ON go_jobs (run_at, id) WHERE finished_at IS NULL;
