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
| `--hostpath` | yes (repeatable) | — | `name:path:interval` — pass once per directory to monitor, e.g. `--hostpath app:/data/app:30s`. `path` must be POSIX-style absolute (leading `/`) and must not contain a colon. `interval` is a Go duration (`30s`, `5m`, `1h`). |
| `--metrics_path` | no | `/metrics` | HTTP path to serve metrics on. |
| `--metrics_port` | no | `9090` | TCP port to serve metrics on. |
| `--loglevel` | no | `info` | One of `debug`, `info`, `warn`, `error`. Unknown values fall back to `info` with a warning. |

Multiple `--hostpath` flags are combined; each may have a different refresh
interval. Duplicate `name` values across `--hostpath` entries are rejected at
startup.

## Metric

```
hostpath_usage_bytes{name="<name>",path="<path>"} <bytes>
```

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

## Logging

Structured logs are written to stderr via `log/slog`. `--loglevel` controls
verbosity:

- `debug` — logs the start of every scan cycle in addition to `info`.
- `info` (default) — startup config, server listen address, scan
  completions (bytes/file count/duration), shutdown lifecycle.
- `warn` — unknown `--loglevel` values, zero `--hostpath` flags configured.
- `error` — scan failures, HTTP server errors.

## Development

```sh
go build ./...
go vet ./...
gofmt -l .
go test ./...
```
