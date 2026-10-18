# Bug: websocket server crash-loops with the current `oneacrefund/superset-websocket:latest` image

## Summary

The operator starts the websocket server with `node websocket_server.js`
(`DefaultCommand` in `internal/controller/component_resources.go`). The current
`oneacrefund/superset-websocket:latest` image, which the docs and the e2e test
use, no longer contains that file. The container exits immediately with
`MODULE_NOT_FOUND` and goes into `CrashLoopBackOff`.

Pointing the command at the new bundle (`node dist/index.cjs`) is not enough:
the process connects to Redis but never starts its HTTP server, so the
`/health` liveness and readiness probes fail and the pod keeps restarting. The
image's own entry point passes a `start` argument. With
`node dist/index.cjs start`, the server listens on 8080, `/health` returns
200, and the pod becomes ready.

## Environment

| | |
|---|---|
| Operator | `main` at `bbbbd30`, run with `make run` |
| Cluster | Kind |
| Websocket image | `oneacrefund/superset-websocket:latest`, digest `sha256:4eb83fd52d4f240a805535de8518fd38857f18ee70b3b87419e0e8b8459eab4e` |
| Node.js in image | v24.21.0 |
| Date observed | 2026-10-17 |

## Steps to reproduce

1. Create a Kind cluster and the dev dependencies (Postgres + Valkey), then
   install the CRD and run the operator:

   ```bash
   kind create cluster --name superset
   kubectl apply -f config/samples/dev-dependencies.yaml
   make install
   make run    # separate terminal
   ```

2. Apply a Development-mode instance with the websocket server, using the image
   from the docs:

   ```yaml
   apiVersion: superset.apache.org/v1alpha1
   kind: Superset
   metadata:
     name: wstest
   spec:
     image:
       tag: 6.1.0-dev
     environment: Development
     secretKey: thisIsNotSecure_changeInProduction!
     metastore:
       uri: postgresql+psycopg2://superset:superset@postgres:5432/superset
     valkey:
       host: valkey
       password: superset
     webServer: {}
     websocketServer:
       image:
         repository: oneacrefund/superset-websocket
         tag: latest
       config:
         port: 8080
         jwtSecret: thisIsNotSecure_devOnly_jwtSecret_0123456789
         jwtCookieName: async-token
         redis:
           host: valkey
           port: 6379
           password: superset
           db: 0
   ```

3. Wait for the lifecycle tasks to finish, then check the websocket pod:

   ```bash
   kubectl get pods | grep websocket-server
   kubectl logs deploy/wstest-websocket-server
   ```

## Observed

The pod is in `CrashLoopBackOff`:

```text
Error: Cannot find module '/home/superset-websocket/websocket_server.js'
    at Module._resolveFilename (node:internal/modules/cjs/loader:1564:15)
    ...
  code: 'MODULE_NOT_FOUND',
  requireStack: []
}

Node.js v24.21.0
```

The image's working directory, `/home/superset-websocket`, contains only a
`dist/` directory with a single bundle, `dist/index.cjs`. There is no
`websocket_server.js` and no `package.json`.

### With `command: ["node", "dist/index.cjs"]`

Setting `websocketServer.podTemplate.container.command` to
`["node", "dist/index.cjs"]` gets past the crash. The process reads the mounted
`config.json` and connects to Redis:

```text
{"level":"info","message":"Redis subscriber healthy; realtime transport available",...}
{"level":"info","message":"Subscribed to Redis channel: realtime",...}
```

But it never opens a listening socket (`/proc/net/tcp` and `/proc/net/tcp6`
list nothing in `LISTEN`), so both probes fail and the kubelet restarts the
container:

```text
Liveness probe failed: Get "http://10.244.0.45:8080/health": dial tcp 10.244.0.45:8080: connect: connection refused
Readiness probe failed: Get "http://10.244.0.45:8080/health": dial tcp 10.244.0.45:8080: connect: connection refused
```

## Cause

Run with no command override, the image's PID 1 is:

```text
node --no-opt -r /proc/.reset dist/index.cjs start
```

The bundle only starts its HTTP server when it receives the `start` argument.
With the image's default entry point (and the same mounted `config.json`):

```text
{"level":"info","message":"Server started on port 8080",...}
{"level":"info","message":"Redis subscriber healthy; realtime transport available",...}
```

Port 8080 is listening and `GET /health` returns `200 OK`. The bundle still
reads `config.json` from `../config.json` relative to `dist/`, which matches
the operator's mount path (`/home/superset-websocket/config.json`), and still
defines the `/health` route on `config.port` (default 8080). Only the entry
point changed.

## Workaround (verified)

Override the command to include `start`:

```yaml
spec:
  websocketServer:
    image:
      repository: oneacrefund/superset-websocket
      tag: latest
    podTemplate:
      container:
        command: ["node", "dist/index.cjs", "start"]
```

Through the operator, with its default probes, the pod became `1/1 Running`
with 0 restarts and logged `Server started on port 8080`.

## Affected places in the repo

| Location | What it does |
|---|---|
| `internal/controller/component_resources.go` | `DefaultCommand: []string{"node", "websocket_server.js"}` |
| `docs/user-guide/configuration.md` | Recommends `oneacrefund/superset-websocket` with `tag: "latest"` |
| `docs/reference/api-reference.md`, CRD descriptions (`config/crd/bases/...`, `charts/superset-operator/crds/...`) | Describe the requirement as an image that contains `websocket_server.js` |
| `test/e2e/superset_journey_test.go` | Uses `oneacrefund/superset-websocket` with `tag: latest` |

## Possible fixes (for discussion)

1. **Don't set a default command for the websocket server.** Use the image's
   own entry point, so the operator doesn't depend on the file layout of an
   external image. Users can still override `command`.
2. **Change the default command** to `["node", "dist/index.cjs", "start"]`.
   This matches the current image but breaks images built with the old layout.
3. **Pin a tag or digest** in the docs and the e2e test instead of `latest`,
   so upstream image changes don't break users without notice.
4. **Update the CRD/API docs wording** that requires `websocket_server.js`.

Options 1 and 3 together would make the operator independent of the image
layout and keep the documented example reproducible.
