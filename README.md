# hostpath-size-exporter

Prometheus exporter that reports the disk usage of one or more `hostPath`
directories, for use in Kubernetes.

## How it works

For each configured `--hostpath`, a background goroutine walks the directory
tree on its own refresh interval and sums the apparent size (regular file
sizes, via `filepath.WalkDir`) of everything under it. Symlinks are not
followed. The result is cached in a Prometheus gauge and served over HTTP —
a scrape of `/metrics` never triggers a filesystem walk itself, it just reads
the last computed value.

A directory's metric only appears in `/metrics` output after its first
successful scan completes. If a scan fails (path not mounted yet, permission
denied, etc.), the error is logged and the gauge is left at its last known
value rather than being reset to zero or removed — a transient error won't
show up as a misleading dip.

## Flags

| Flag | Required | Default | Description |
|---|---|---|---|
| `--hostpath` | yes, unless `--auto` (repeatable) | — | `name:path:interval` — pass once per directory to monitor, e.g. `--hostpath app:/data/app:30s`. `path` must be POSIX-style absolute (leading `/`) and must not contain a colon. `interval` is a Go duration (`30s`, `5m`, `1h`). |
| `--metrics_path` | no | `/metrics` | HTTP path to serve metrics on. |
| `--metrics_port` | no | `9090` | TCP port to serve metrics on. |
| `--loglevel` | no | `info` | One of `debug`, `info`, `warn`, `error`. Unknown values fall back to `info` with a warning. |
| `--auto` | no | `false` | Auto-discover hostPath volumes of pods on this node (see [Auto mode](#auto-mode)). Must run as a DaemonSet pod. Mutually exclusive with `--hostpath`. |
| `--host_root` | no | `/host` | Auto mode: where the host's `/` is mounted inside the container. A discovered hostPath `/x` is scanned at `<host_root>/x`. |
| `--auto_interval` | no | `60s` | Auto mode: scan interval for every discovered hostPath. |
| `--auto_refresh` | no | `30s` | Auto mode: how often pods on the node are re-listed. |
| `--auto_exclude` | no (repeatable) | — | Auto mode: host path that is never scanned, together with everything under it. Exception: `--auto_exclude=/` excludes only the root path itself (e.g. a node-exporter `/` mount), not every path. |

Multiple `--hostpath` flags are combined; each may have a different refresh
interval. Duplicate `name` values across `--hostpath` entries are rejected at
startup.

## Metric

```
hostpath_usage_bytes{name="<name>",path="<path>",namespace="<ns>",pod="<pod>",volume="<volume>"} <bytes>
```

- With `--hostpath`: `name` and `path` come from the flag; `namespace`, `pod`
  and `volume` are empty.
- With `--auto`: `name` is `<namespace>/<pod>/<volume>`, and `path` is the
  hostPath as declared in the pod spec (the host path, without `--host_root`).
  When several pod volumes declare the same host path, the directory is
  scanned once and one series is published per pod volume. Keep that in mind
  when summing.
- The `namespace`, `pod` and `volume` labels were added together with
  `--auto`. Queries or alerts that match on the exact old label set
  (`{name,path}`) may need updating.

- Type: gauge.
- `<bytes>` is the sum of regular file sizes under `<path>` as of the most
  recent successful scan for that hostpath.
- Updated independently per hostpath, on that hostpath's own `--hostpath`
  interval.

## Example invocation

```sh
hostpath-size-exporter \
  --hostpath app:/data/app:30s \
  --hostpath cache:/data/cache:5m \
  --metrics_path /metrics \
  --metrics_port 9090 \
  --loglevel info
```

## Docker

Build:

```sh
docker build -t hostpath-size-exporter .
```

Run, mounting a host directory read-only:

```sh
docker run --rm -p 9090:9090 \
  -v /host/data:/data:ro \
  hostpath-size-exporter \
  --hostpath app:/data:60s
```

The image runs as a non-root user and never writes to mounted paths, so
`:ro` mounts are sufficient and recommended.

## Kubernetes example

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: hostpath-size-exporter
spec:
  selector:
    matchLabels:
      app: hostpath-size-exporter
  template:
    metadata:
      labels:
        app: hostpath-size-exporter
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "9090"
        prometheus.io/path: "/metrics"
    spec:
      containers:
        - name: hostpath-size-exporter
          image: hostpath-size-exporter:latest
          args:
            - --hostpath=app:/data/app:60s
            - --hostpath=logs:/data/logs:5m
          ports:
            - containerPort: 9090
              name: metrics
          volumeMounts:
            - name: app-data
              mountPath: /data/app
              readOnly: true
            - name: logs-data
              mountPath: /data/logs
              readOnly: true
      volumes:
        - name: app-data
          hostPath:
            path: /var/lib/app
            type: Directory
        - name: logs-data
          hostPath:
            path: /var/log/app
            type: Directory
```

## Auto mode

With `--auto`, the exporter finds hostPath volumes itself instead of taking
`--hostpath` flags:

1. At startup it fetches its own pod. It reads `POD_NAME` / `POD_NAMESPACE`
   from the downward API, falling back to the hostname and the service account
   namespace. It **exits with an error unless the pod is controlled by a
   DaemonSet**, and also exits with an error if it is not running inside a
   cluster at all.
2. Every `--auto_refresh` it lists pods on its own node
   (`spec.nodeName=<node>`) across all namespaces and collects their
   `hostPath` volumes. It skips:
   - its own pod;
   - pods in phase `Succeeded` or `Failed`;
   - hostPath types `File`, `FileOrCreate`, `Socket`, `CharDevice` and
     `BlockDevice`;
   - `--auto_exclude` paths.
3. Each distinct host path gets one scanner goroutine, which walks
   `<host_root>/<path>` every `--auto_interval`. When the last pod
   declaring a path goes away, scanning stops and the path's series are
   removed. When one of several pods goes away, only that pod's series is
   removed.

If listing pods fails, the error is logged and the current set of paths is
still scanned.

> A pod that mounts the host `/` (node exporters, log shippers, CSI drivers)
> would make the exporter walk the whole host filesystem. Use
> `--auto_exclude=/` (and exclude any other large paths) to avoid that.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: hostpath-size-exporter
  namespace: monitoring
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: hostpath-size-exporter
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: hostpath-size-exporter
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: hostpath-size-exporter
subjects:
  - kind: ServiceAccount
    name: hostpath-size-exporter
    namespace: monitoring
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: hostpath-size-exporter
  namespace: monitoring
spec:
  selector:
    matchLabels:
      app: hostpath-size-exporter
  template:
    metadata:
      labels:
        app: hostpath-size-exporter
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "9090"
        prometheus.io/path: "/metrics"
    spec:
      serviceAccountName: hostpath-size-exporter
      containers:
        - name: hostpath-size-exporter
          image: hostpath-size-exporter:latest
          args:
            - --auto
            - --auto_interval=5m
            - --auto_exclude=/
          env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
          ports:
            - containerPort: 9090
              name: metrics
          volumeMounts:
            - name: host-root
              mountPath: /host
              readOnly: true
              mountPropagation: HostToContainer
      volumes:
        - name: host-root
          hostPath:
            path: /
            type: Directory
```

`mountPropagation: HostToContainer` makes filesystems that are mounted on the
host after the exporter starts visible under `/host`. The container needs read
access to the directories it scans; the image runs as root by default.

## Logging

Structured logs are written to stderr via `log/slog`. `--loglevel` controls
verbosity:

- `debug` — logs the start of every scan cycle in addition to `info`.
- `info` (default) — startup config, server listen address, scan
  completions (bytes/file count/duration), shutdown lifecycle.
- `warn` — unknown `--loglevel` values, zero `--hostpath` flags configured.
- `error` — scan failures, HTTP server errors, auto mode startup failures
  (not in a cluster, not a DaemonSet pod), pod listing failures.

## Development

```sh
go build ./...
go vet ./...
gofmt -l .
go test ./...
```
