FROM golang:1.26-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/hostpath-size-exporter .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/hostpath-size-exporter /hostpath-size-exporter

# Runs as the distroless "nonroot" user by default. The container needs no
# write access to any mounted hostPath volume — mount them read-only
# (volumeMounts[].readOnly: true) in your Kubernetes manifest.
EXPOSE 9090

ENTRYPOINT ["/hostpath-size-exporter"]
