-- Moves the pending Solid Queue jobs to go_jobs. Run once, with Rails stopped:
--   serve-html-markdown move-solid-queue-jobs        (or psql -f, on port 5432)
--
-- Rails' workers are gone, so a job that was claimed by one is orphaned: it is
-- moved too. The Go handlers are safe to run again after a partial run.
-- Failed jobs stay where they are. Timestamps are UTC, like the app writes them.
BEGIN;

INSERT INTO go_jobs (kind, args, run_at)
SELECT CASE j.class_name
         WHEN 'GenerateExpansionJob'    THEN 'generate_expansion'
         WHEN 'TwitterVideoIngestJob'   THEN 'twitter_video_ingest'
         WHEN 'TwitterVideoCaptionsJob' THEN 'twitter_video_captions'
       END,
       jsonb_build_object('id', (j.arguments::jsonb -> 'arguments' ->> 0)::bigint),
       COALESCE(j.scheduled_at AT TIME ZONE 'UTC', now())
FROM solid_queue_jobs j
WHERE j.finished_at IS NULL
  AND j.class_name IN ('GenerateExpansionJob', 'TwitterVideoIngestJob', 'TwitterVideoCaptionsJob')
  AND NOT EXISTS (SELECT 1 FROM solid_queue_failed_executions f WHERE f.job_id = j.id);

UPDATE solid_queue_jobs
   SET finished_at = now() AT TIME ZONE 'UTC'
 WHERE finished_at IS NULL
   AND NOT EXISTS (SELECT 1 FROM solid_queue_failed_executions f WHERE f.job_id = solid_queue_jobs.id);

-- An expansion Rails was generating when it was stopped will not finish.
UPDATE expansions
   SET status = 'failed', error_detail = 'Interrupted by server migration.',
       updated_at = now() AT TIME ZONE 'UTC'
 WHERE status = 'processing';

COMMIT;
