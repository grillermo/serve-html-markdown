-- Solid Queue is gone: the Go server runs its jobs from go_jobs. Its tables hold
-- only finished-job history (the cutover moved the pending jobs, see the Go
-- migration plan). IF EXISTS because a database created from db/schema.sql
-- after this point may never have had them.
DROP TABLE IF EXISTS
  solid_queue_blocked_executions,
  solid_queue_claimed_executions,
  solid_queue_failed_executions,
  solid_queue_pauses,
  solid_queue_processes,
  solid_queue_ready_executions,
  solid_queue_recurring_executions,
  solid_queue_recurring_tasks,
  solid_queue_scheduled_executions,
  solid_queue_semaphores,
  solid_queue_jobs
CASCADE;
