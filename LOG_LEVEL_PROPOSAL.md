# Proposal: unified component log level (`spec.logLevel`)

Draft for maintainer review. No code has been written yet.

## Summary

Add one `spec.logLevel` field (default `info`), inherited by every component and
lifecycle task, with a per-component `logLevel` override. The operator translates
the resolved level into each component's own mechanism. Every mechanism below was
verified by hand on a local Kind cluster (operator `main` at `bbbbd30`, Superset
`6.1.0-dev`, Celery 5.5.2), using command overrides and component `config`.

## Proposed API

```go
// +kubebuilder:validation:Enum=debug;info;warning;error;critical
type LogLevel string
```

```yaml
spec:
  logLevel: warning        # inherited by all components and lifecycle tasks
  celeryWorker:
    logLevel: debug        # overrides for this component only
```

- Resolution: component `logLevel`, then `spec.logLevel`, then `info`. No
  kubebuilder default, so unset stays distinguishable from an explicit `info`.
- The field sits on each component spec (`celeryWorker.logLevel`), not under
  `celeryWorker.celery` as in the #341 prototype.
- `LOG_LEVEL` set in `spec.config` or a component's `config` still wins, because
  the operator renders its own value before those sections.
- A user `command` override replaces the whole operator-built command (verified),
  so it also drops the operator's log flags. This will be documented.

## Component mapping

| Component | Mechanism | Verified result |
|---|---|---|
| Celery worker | `--loglevel=<level>` in `ResolvedCelery.Command()` | Fixes the silent worker |
| Celery Beat | `--loglevel=<level>` appended to the default Beat command | Fixes the silent Beat |
| Web server, MCP server, lifecycle tasks | `LOG_LEVEL = "<LEVEL>"` in `superset_config.py` | Works in both directions for Superset's loggers |
| Web server (Gunicorn) | `GUNICORN_LOGLEVEL`, in addition to `LOG_LEVEL` | Controls Gunicorn's messages, not its access log (Q5) |
| Celery Flower | `LOG_LEVEL` **and** `--logging=<level>` | `LOG_LEVEL` alone leaves Flower's own lines at INFO |
| Websocket server | `LOG_LEVEL` env; `warning` → `warn`, `critical` → `error` | Mapping is required (see Findings) |

Behavior change for the changelog: with the default `info`, the worker and Beat
will log task activity that is silent today.

## Findings

**Celery worker and Beat.** Neither command has `--loglevel`, so Celery applies
its default WARNING to the root logger at startup, which also silences Superset's
loggers. Over 24 minutes the worker ran 25 `reports.scheduler` tasks
(`celery inspect stats`) and logged none of them; Beat logged nothing after app
init. With `--loglevel=info` added by hand:

```text
[14:51:00,053: INFO/MainProcess] Task reports.scheduler[db23b4f0-...] received
[14:51:00,076: INFO/ForkPoolWorker-1] Task reports.scheduler[db23b4f0-...] succeeded in 0.02s: None
[15:21:00,042: INFO/MainProcess] Scheduler: Sending due task reports.scheduler (reports.scheduler)
```

**Web server.** One `GET /login/` request per run.

| Setting | Superset DEBUG / INFO | Gunicorn `[INFO]` | Access log |
|---|---|---|---|
| default | 0 / 70 | 5 | present |
| `LOG_LEVEL = "DEBUG"` | 24 / 70 | 5 | present |
| `LOG_LEVEL = "WARNING"` | 0 / 0 | 5 | present |
| `GUNICORN_LOGLEVEL=warning` | 0 / 70 | 0 | **present** |

**Lifecycle tasks.** `LOG_LEVEL = "DEBUG"` in `lifecycle.config` added 10 DEBUG
lines to the init task. Adding it and removing it each **re-ran `superset init`**
(about 8 seconds, no drain); migrate did not re-run (Q2).

**Flower.** `LOG_LEVEL = "WARNING"` removed Superset's 35 INFO lines, but
Flower's own lines (`Visit me at ...`, `Broker: ...`) and kombu's stayed at INFO
until `--logging=warning` was added.

**MCP server.** `LOG_LEVEL` works both ways (WARNING: 47 → 0 INFO lines; DEBUG:
+28 lines, including `httpcore` details of outgoing HTTP calls). `server.py`
only changes logger levels in `--debug` mode, which the operator doesn't use.
The FastMCP banner and Uvicorn's `INFO:` startup lines print at every level (Q7).

**Websocket server** (`oneacrefund/superset-websocket:latest`). `LOG_LEVEL=warn`
hid the INFO lines. With Redis made unreachable on purpose, `warn` logged 10
`error` lines, but `warning` logged **nothing at all**: winston has no `warning`
level, and an unknown name silences everything, including errors. `debug` was not
confirmed (no client was connected). Separately, the operator's default websocket
command crash-loops with this image; see `WEBSOCKET_CRASH_REPORT.md`.

## Open questions

The answers to Q1, Q2 and Q6 change the code most.

**Q1. Keep or remove `webServer.gunicorn.logLevel`?**
It already sets Gunicorn's level, so with the new fields a user could write
`webServer.logLevel: debug` and `webServer.gunicorn.logLevel: warning`. Options:
keep it as the most specific override (`gunicorn.logLevel` > `webServer.logLevel`
> `spec.logLevel`, nothing breaks, but two knobs for one component), or remove it
in 0.4.0 so `webServer.logLevel` controls both (cleaner, breaking). Lean: remove.

**Q2. Should a log-level change re-run `superset init`?**
The init task re-runs whenever its rendered config changes
(`lifecycle_init.go`, `ConfigHash`), so `LOG_LEVEL` there triggers a re-run that
re-syncs roles and permissions. It's quick and caused no downtime, but it's
surprising for a logging setting. Options: accept it, or exclude the log level
from the init checksum (the new level then reaches the tasks on their next run).
Lean: exclude.

**Q3. How to flag `debug` in Production?**
Debug output can include SQL and HTTP call details, but debugging production is
sometimes necessary, and CEL can only reject. Options: a Warning Event (like the
existing `SeedEnvironmentNotAllowed`), a status condition such as
`DebugLoggingEnabled`, or both. Lean: both, since Events expire after about an
hour and a condition lasts while debug is on.

**Q4. Add `--loglevel` when the Celery preset is `disabled`?**
`preset: disabled` uses a hardcoded fallback command the new code wouldn't touch,
so those workers would stay silent. Options: add `--loglevel` to the fallback too,
or treat `disabled` as "hands off" and document it. Lean: add it, since the log
level is separate from the tuning that `disabled` turns off.

**Q5. Leave Gunicorn's access log on at every level?**
`GUNICORN_LOGLEVEL` doesn't affect it, so a line per request (including the
kubelet `/health` probe every few seconds) remains at `warning`. Options: leave it
on and document it, or turn it off above `info` through Gunicorn's access-log
setting. Lean: leave it on, since some users rely on access logs.

**Q6. Restart every component on a log-level change, or only the affected ones?**
Settings in the shared config checksum restart all components: changing
`webServer.gunicorn.logLevel` also restarted the worker and Beat. If `logLevel`
joins that checksum, `celeryWorker.logLevel: debug` would restart everything.
Options: shared checksum (simplest), or per component (each restarts only when
its own resolved level changes; this mostly comes for free, since the level ends
up in each component's own config or command). Lean: per component.

**Q7. MCP's Uvicorn lines: document, or raise upstream?**
`superset mcp run` only has `--host`, `--port` and `--debug`, so the operator
can't set Uvicorn's level; about 4 startup lines stay at INFO. Options: document
it, or ask Superset for a log-level option and use it later. Lean: document now.

**Q8. How to add `--logging` to Flower's command?**
Flower's command is one shell string (it sources the bootstrap script first),
unlike the worker's argument list. Options: append `--logging=<level>` to the
string (small and safe, since the value is one of five enum values), or give
Flower a command builder like the worker's (cleaner, bigger change). Lean:
append now; a builder can come later.
