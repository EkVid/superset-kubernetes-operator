Thanks for assigning this to me! @villebro, before implementing I'd like to clarify a few design choices. I tested each component's mechanism by hand on a Kind cluster, and the results mostly confirm the proposed mapping. Two additions: Flower also needs `--logging=<level>`, because `LOG_LEVEL` alone leaves Flower's own lines at INFO, and the websocket `warning` → `warn` mapping is required, because winston silences all output, including errors, when it gets an unknown level name.

Q1, Q2 and Q6 affect the implementation most.

---

**Q1. Should `webServer.gunicorn.logLevel` stay?**

The issue leaves this open. The field already sets `GUNICORN_LOGLEVEL`, so with the new fields a user could set `webServer.logLevel: debug` and `webServer.gunicorn.logLevel: warning` at the same time.

- **Keep it** as the most specific override: `gunicorn.logLevel` > `webServer.logLevel` > `spec.logLevel`.
- **Remove it**, so `webServer.logLevel` sets both `LOG_LEVEL` and `GUNICORN_LOGLEVEL`.

My suggestion: remove it. It's cleaner, and since 0.4.0 allows breaking changes, this seems like the right time.

---

**Q2. Should a log-level change re-run `superset init`?**

The init task's checksum includes its rendered `superset_config.py` (`ConfigHash` in `lifecycle_init.go`). To simulate the operator rendering `LOG_LEVEL` into the task config, I added `LOG_LEVEL = "DEBUG"` to `lifecycle.config`. This re-ran `superset init`, and removing it re-ran init again. Each run took about 8 seconds and caused no drain. Migrate was not affected.

- **Keep it as is**: every log-level change re-runs init.
- **Exclude the log level from the init checksum**: no re-run; the new level applies the next time init runs.

My suggestion: exclude it, since re-syncing roles and permissions for a logging change is surprising.

---

**Q3. Warning for `debug` outside Development**

Following the issue's open question, I plan to warn rather than reject. Testing confirmed the concern: on the MCP server, DEBUG adds `httpcore` logs of outgoing HTTP calls. Two follow-ups:

- Should it be **both** a Warning Event and a status condition (e.g. `DebugLoggingEnabled=True`)? Events expire after about an hour, while a condition stays visible as long as debug is on.
- Should the warning apply only to `environment: Production`, as the issue says, or to `Staging` as well?

My suggestion: both signals, and warn for Staging too.

---

**Q4. Should `--loglevel` be added when the Celery preset is `disabled`?**

With `celeryWorker.celery.preset: disabled`, the worker uses the hardcoded fallback command in `component_resources.go`. Without `--loglevel` there, those workers would stay silent, which is the bug this issue fixes.

- **Add it** to the fallback command as well.
- **Leave it**, treating `disabled` as "the operator doesn't touch the command", and document it.

My suggestion: add it, since the log level is separate from the tuning that `disabled` turns off.

---

**Q5. Should Gunicorn's access log stay on at every level?**

`GUNICORN_LOGLEVEL=warning` removes Gunicorn's own `[INFO]` lines but not the access log. A line per request still prints, including the kubelet `/health` probe every few seconds.

- **Leave it on** and document that request lines always appear.
- **Turn it off above `info`** through Gunicorn's access-log setting.

My suggestion: leave it on, since some users rely on access logs.

---

**Q6. Should a log-level change restart every component, or only the affected ones?**

`webServer.gunicorn` is part of the shared config checksum (`superset_controller.go`) that every component's pods carry. In testing, changing `webServer.gunicorn.logLevel` restarted the worker and Beat as well as the web server. If `logLevel` joins that checksum, `celeryWorker.logLevel: debug` would restart every component.

- **Shared checksum**: simplest, but restarts everything.
- **Per component**: each component restarts only when its own resolved level changes. This mostly comes for free, since the level ends up in each component's own config or command.

My suggestion: per component. Also, is Gunicorn being in the shared checksum intended?

---

**Q7. MCP server: document Uvicorn's lines, or raise it upstream?**

The issue asked to check the MCP server. `LOG_LEVEL` works for Superset's loggers there, and `server.py` only changes logger levels in `--debug` mode, which the operator doesn't use. However, the FastMCP banner and about 4 Uvicorn `INFO:` startup lines print at every level, and `superset mcp run` only accepts `--host`, `--port` and `--debug`, so the operator can't set Uvicorn's level.

- **Document it** as a known limitation.
- **Ask upstream** for a log-level option on `superset mcp run`, and use it later.

My suggestion: document it now.

---

That's everything I've found so far. I'm still fairly new to Kubernetes operators, so I may have missed something. I'd appreciate your thoughts on these questions or anything else I should consider, and I'm happy to share my full test notes and logs if they'd help.
