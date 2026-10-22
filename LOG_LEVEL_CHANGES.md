# Notes
The core is done: the API fields, level resolution, Superset's `LOG_LEVEL`, Gunicorn, and Celery worker/Beat. The debug warning, the Flower/MCP/websocket mappings, tests, and docs are not (see [Remaining work](#remaining-work)).

## Why

Before this change there was no single knob for component log levels, and the defaults were inconsistent:

- The Celery worker and Beat ran without `--loglevel`. Celery then applies its default WARNING to the root logger at startup, which also silences Superset's own INFO logs. On Kind, the worker ran 25 `reports.scheduler` tasks in 24 minutes and logged none of them; with `--loglevel=info` it logs `Task ... received` and `succeeded`.
- `webServer.gunicorn.logLevel` only covered Gunicorn, not Superset's `LOG_LEVEL`.
- Superset's `LOG_LEVEL` and the other components' levels could only be changed through raw `config` or env overrides.

The change adds one `spec.logLevel` field with per-component and lifecycle overrides, and maps the resolved level onto each component's own logging mechanism. The design follows the maintainer's answers on the issue.

## What it does

```yaml
spec:
  logLevel: warning        # all components and lifecycle tasks
  celeryWorker:
    logLevel: debug        # this component only
  lifecycle:
    logLevel: info         # all lifecycle tasks
```

Resolution: component (or lifecycle) value, then `spec.logLevel`, then `info`. Allowed values: `debug`, `info`, `warning`, `error`, `critical`.

| Component | Mechanism | Reasons |
|---|---|---|
| Web server, worker, Beat, Flower, MCP, lifecycle tasks | `LOG_LEVEL = os.environ.get('SUPERSET_OPERATOR__LOG_LEVEL', 'INFO')` in `superset_config.py`, plus the env var on the pod | The rendered file is identical at every level, so a level change never changes a lifecycle task checksum. So `superset init` does not get triggered everytime a log level has been set  |
| Web server | `GUNICORN_LOGLEVEL` | `LOG_LEVEL` doesn't reach Gunicorn (verified on Kind) |
| Worker, Beat | `--loglevel=<level>` | Celery resets the root logger at startup, overriding `LOG_LEVEL` (verified on Kind) |

## Design decisions based on the answers provided by Ville

| Decision | Reason |
|---|---|
| Remove `webServer.gunicorn.logLevel` (breaking) | Since 0.4.0 allows breaking changes, `LOG_LEVEL` in `webServer.config` can set Superset's level apart from Gunicorn's |
| Level is not in the shared config checksum | Each level reaches only its own pod template (env var or command arg), so changing one component's level restarts only that component |
| Level travels as an env var, not a rendered value | Keeps `ConfigHash` stable. The env var is added in `buildOperatorInjected`, outside `collectSecretEnvVars`, so init's `EnvHash` stays stable too. Init no longer gets triggered on a level change |
| No kubebuilder default on any `logLevel` | A defaulted per component value would always win over `spec.logLevel`; `info` is applied at resolution |
| `logLevel` on `ComponentSpec`, once on `LifecycleSpec` | Every component embeds `ComponentSpec`; one lifecycle field covers all tasks |
| `--loglevel` and `GUNICORN_LOGLEVEL` even with `preset: disabled` | The level is separate from the tuning that `disabled` turns off |
| User set `podTemplate.container.command` stays as it is | Existing behavior; a custom command must pass `--loglevel` itself |

## Changes by file

### API types

| File | Change |
|---|---|
| `api/v1alpha1/shared_types.go` | New `LogLevel` type with the enum marker; `LogLevel` field on `ComponentSpec`; removed `GunicornSpec.LogLevel` |
| `api/v1alpha1/superset_types.go` | `SupersetSpec.LogLevel` and `LifecycleSpec.LogLevel` |

### Generated from `make codegen`. We don't edit these by hand

`api/v1alpha1/zz_generated.deepcopy.go`, `config/crd/bases/superset.apache.org_supersets.yaml`, `charts/superset-operator/crds/superset.apache.org_supersets.yaml`, `docs/reference/api-reference.md`.

### Config package
| File | Change |
|---|---|
| `internal/config/loglevel.go` (newly added) | `ResolveLogLevel` and translators (`PythonLogLevel`, `CeleryLogLevelFlag`) |
| `internal/config/renderer.go` | Renders the fixed `LOG_LEVEL` line after `SECRET_KEY`, before `spec.config` so a user value still wins |
| `internal/config/gunicorn.go` | Removed `LogLevel` from `ResolvedGunicorn` and its env vars. The controller injects `GUNICORN_LOGLEVEL` instead |
| `internal/common/names.go` | `EnvLogLevel = "SUPERSET_OPERATOR__LOG_LEVEL"` |

### Controller

| File | Change |
|---|---|
| `internal/controller/loglevel.go` (newly added)| `appendLogLevelEnv` (currently only `GUNICORN_LOGLEVEL`; Teammate 2 adds the other components here). `withCeleryDefaultLogLevel` (adds `--loglevel` to the worker fallback and Beat default commands). `lifecycleLogLevel` |
| `internal/controller/component_descriptors.go` | `logLevel` on `componentAccessor`, `extractScalable` reads it from `ComponentSpec`. `reconcileComponent` resolves the level once and applies it. The per component logic is refactored into `loglevel.go` because the linter thinks that `reconcileComponent` will be too complex |
| `internal/controller/config_builder.go` | `buildOperatorInjected` takes a `logLevel` and injects `SUPERSET_OPERATOR__LOG_LEVEL` next to `FORCE_RELOAD` |
| `internal/controller/lifecycle_taskspec.go` | Lifecycle task pods get the resolved lifecycle level |

### Existing tests (adjusted only so they compile and pass, might need to look again after all changes are done)

| File | Change |
|---|---|
| `internal/config/gunicorn_test.go` | Removed the assertions on the deleted `LogLevel` field; Gunicorn now returns 9 env vars instead of 10 |
| `internal/controller/config_test.go` | Passes the new `logLevel` argument (`""`) to `buildOperatorInjected` |



## Remaining work

### Debug Warning

Debug output can contain sensitive data, so Ville asked to flag `debug` outside Development:

- Add a `DebugLogging` condition type next to the others in `api/v1alpha1/shared_types.go`.
- Set it while any component or lifecycle task resolves to `debug` and `environment` isn't `Development` (Staging counts too, since it can hold production data). Name the affected components in the message, and remove it when debug is turned off. `Suspended` in `superset_controller.go` is the precedent.
- It must not affect `Ready`/`Available`.
- Emit a Warning Event only when the condition first appears, not on every reconcile.
- Only the typed `logLevel` fields are detected, not `LOG_LEVEL = "DEBUG"` written in `config`; document that.
- Helpers to reuse: `supersetconfig.ResolveLogLevel`, `lifecycleLogLevel`, `isDevEnvironment`, `setCondition`/`removeCondition`.


### Flower, MCP server, websocket server

These components already get Superset's `LOG_LEVEL`, but their own loggers ignore it. Add a case for each in `appendLogLevelEnv` (`internal/controller/loglevel.go`):

| Component | What to inject | Notes |
|---|---|---|
| Flower | `FLOWER_LOGGING` env | `LOG_LEVEL` leaves Flower's own lines at INFO (verified on Kind). Flower reads `FLOWER_*` env vars before parsing its command line, so its shell command shouldn't need editing. Ville suggested mapping `critical` to `error`, but Tornado's code does `getattr(logging, value.upper())`, which seems to accept `critical`; check on Kind |
| MCP server | `FASTMCP_LOG_LEVEL` env (uppercase) | FastMCP applies it to its loggers and passes it to Uvicorn in single pod mode. Never map `debug` to `superset mcp run --debug` (it logs SQL text). Document that multi-pod mode (`MCP_EVENT_STORE_CONFIG`) doesn't pass the level to Uvicorn |
| Websocket server | `LOG_LEVEL` env, `warning` → `warn`, `critical` → `error` | winston has no `warning` level; an unknown name silenced all output, including errors (verified on Kind). Inject only when `spec.logLevel` or `websocketServer.logLevel` is set explicitly, because the operator's env var would otherwise override a `logLevel` in the user's `config.json` |

To run the websocket server on Kind, override its command to `["node", "dist/index.cjs", "start"]` until Teammate 1's fix lands.

**Optional upstream task:** in MCP multi-pod mode, Superset starts Uvicorn without a level. Ville said an upstream PR to `apache/superset` that passes the level through would be welcome.

### Tests and Docs

- **Tests** for the core (the API fields, resolution, Superset's `LOG_LEVEL`, Gunicorn, Celery worker/Beat), including the two behaviors Ville asked for: a level change restarts only the affected component, and doesn't re-run `superset init`. Plus envtest cases in `cel_validation_test.go` for the enum validation and the missing defaults.
- **Docs** for the core: the "Log Level" section and mapping table in `docs/user-guide/configuration.md`, the `SUPERSET_OPERATOR__LOG_LEVEL` row in the reserved env var table, the Gunicorn and Celery section updates, `docs/architecture/overview.md`, and `AGENTS.md`.
- **Release notes**: the feature, the **Breaking:** entry for `webServer.gunicorn.logLevel` shaped like #462, and the upgrade effect (item 4 below).

### More Notes:



3. **Kind checks** with `make install && make run`:
   - worker and Beat commands carry `--loglevel`, and task lines appear in the worker logs
   - Flower with `critical` (Teammate 2)
   - `FASTMCP_LOG_LEVEL=WARNING` hides Uvicorn's `INFO:` lines (Teammate 2)
   - `ACCESS_LOG_FILE=/dev/null` turns off the Gunicorn access log
4. **Upgrade effect to note in the release notes (mentioned by Ville):** after upgrading the operator, every rendered `superset_config.py` gains the new `LOG_LEVEL` line. That changes each component's config checksum, so every component's pods restart once (a normal rolling restart), and it changes the init task's checksum, so `superset init` runs once. This happens only at upgrade: the line itself never changes afterwards, because the level is passed through the `SUPERSET_OPERATOR__LOG_LEVEL` env var. Ville said this is fine and asked for it to be mentioned in the release note. So I guess we just document it somewhere.
