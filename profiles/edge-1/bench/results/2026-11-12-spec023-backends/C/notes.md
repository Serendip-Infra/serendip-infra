# C — 2026-11-13T18:07:27Z
- install (SigNoz, k8s-infra): 271 s
obs-load-1: 100 lines in 10 s
obs-load-2: 100 lines in 10 s
obs-load-3: 100 lines in 10 s
obs-load-4: 100 lines in 10 s
obs-load-5: 100 lines in 10 s
- note: the C driver first started without its query env (a load.sh bug, fixed) and was restarted with it at 18:31:46, about 1 min into the load phase; its queries before that failed without reaching ClickHouse
- query results over the run:
  - metrics: 138 queries, 0 not 2xx, mean 0.023 s, max 0.091 s
  - logs: 413 queries, 0 not 2xx, mean 0.047 s, max 0.120 s
- uninstall: 74 s
