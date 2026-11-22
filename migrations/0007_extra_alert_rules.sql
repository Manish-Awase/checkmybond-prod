-- migrations/0007_extra_alert_rules.sql
--
-- Adds latency / throughput / performance-decline rules on top of the 12
-- seeded in 0004. Safe to re-run (ON CONFLICT DO NOTHING).
--
-- Written for a product whose normal traffic is NOT known yet, so:
--   * Rules that don't depend on traffic volume (stuck jobs, slow
--     extraction, DB pool saturation) are ACTIVE.
--   * The throughput-drop rule IS NOT active until you've measured a
--     baseline (see the query at the bottom), because any fixed number
--     picked blindly will either spam you at night or never fire.
-- Tune everything later from /admin/alerts -- no redeploy needed.

INSERT INTO alert_rules
  (id, name, description, metric, comparator, threshold, severity,
   window_seconds, for_seconds, cooldown_seconds, is_active, created_by)
VALUES
  -- Performance decline: work is waiting far too long.
  ('rule_oldest_pending', 'Stuck extraction job',
   'Oldest unfinished extraction job has been waiting longer than this many minutes.',
   'oldest_pending_job_minutes', 'gt', 60, 'critical', 60, 120, 3600, true, 'seed7'),

  -- Latency: end-to-end extraction time getting slow.
  ('rule_extract_slow', 'Extraction p95 duration',
   '95th percentile extraction time (seconds) over the last 30 min.',
   'extraction_p95_seconds', 'gt', 300, 'warning', 1800, 300, 3600, true, 'seed7'),

  -- Latency: DB connection pool nearly exhausted -> everything queues.
  ('rule_db_pool', 'DB connection pool saturated',
   'Connections in use / pool max (0-1). Sustained high = every endpoint is waiting.',
   'db_pool_utilization', 'gt', 0.85, 'warning', 120, 120, 1800, true, 'seed7'),

  -- Latency, tail: p99 is where users actually feel slowness.
  ('rule_api_latency_p99', 'DB query p99 latency',
   'p99 time inside Postgres queries (ms).',
   'db_query_p99_ms', 'gt', 1500, 'warning', 300, 180, 1800, true, 'seed7'),

  -- Throughput: traffic disappeared. INACTIVE until you set a real number.
  ('rule_throughput_drop', 'Request volume dropped',
   'Requests per minute (from audit_log) fell below the threshold. Set threshold to ~30-50% of your normal off-peak rate, then activate.',
   'http_requests_per_min', 'lt', 1, 'warning', 900, 300, 3600, false, 'seed7')
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------------------
-- HOW TO PICK THE THROUGHPUT THRESHOLD (run after ~7 days of real traffic):
--
--   SELECT date_trunc('hour', timestamp) AS hour,
--          round(count(*) / 60.0, 2)     AS avg_req_per_min
--   FROM audit_log
--   WHERE timestamp >= now() - interval '7 days'
--   GROUP BY 1 ORDER BY 1;
--
-- Look at the quietest normal hours (not outages). Set the rule's
-- threshold to about half of that number in /admin/alerts, then switch
-- the rule to active. If your site is legitimately silent at night,
-- keep the rule off or raise window_seconds to 3600.
-- ---------------------------------------------------------------------------
