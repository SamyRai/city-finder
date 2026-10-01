# syntax=docker/dockerfile:1

# city-finder server image.
#
# Stage 1 compiles a fully static server binary (the dependency set is pure
# Go, so CGO_ENABLED=0 holds; verified by `file` on the built binary).
# Stage 2 ships it on distroless/static-debian12:nonroot, which includes
# ca-certificates and tzdata but no shell, no package manager, and no
# nonroot-writable directories — the layout below accounts for all three.

ARG GO_VERSION=1.26

FROM golang:${GO_VERSION} AS builder
# Pin the toolchain to the builder image; never download another one.
ENV GOTOOLCHAIN=local
WORKDIR /src

# Module layer first so source edits do not invalidate the download cache.
COPY go.mod go.sum ./
RUN --mount=type=cache,id=cityfinder-gomod,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,id=cityfinder-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=cityfinder-gobuild,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/cityfinder ./cmd/server

# /out/data becomes /data in the runtime image: the datasets mount point,
# owned by the nonroot user so first-boot downloads can create datasets/
# inside it. distroless has no shell, so directories cannot be created in the
# final stage; the README marker also guarantees the directory survives COPY.
RUN mkdir -p /out/data && \
    printf 'Mount point for the datasets volume (emptyDir or PVC).\nSafe to delete; the server creates datasets/ inside it on first boot.\n' \
    > /out/data/README.txt

FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev
LABEL org.opencontainers.image.title="city-finder" \
      org.opencontainers.image.description="Nearest-city lookup HTTP API over GeoNames data (S2 geometry)" \
      org.opencontainers.image.source="https://github.com/SamyRai/cityFinder" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

# Every path the server consumes is absolute —
# CONFIG_PATH=/etc/cityfinder/config.json (below) and
# datasets_folder=/data/datasets (deploy/config.json) — so config loading
# performs no path discovery of any kind. (v1.0 resolved relative paths via a
# go.mod walk-up and hard-failed without one, which forced a fake /app/go.mod
# marker into this image; lib/config dropped that dependency in v1.1.) A
# relative CONFIG_PATH override resolves against /app, and a relative
# datasets_folder against the config file's directory.

# Default configuration: absolute paths only, datasets under the /data volume.
# Override by mounting your own file and pointing CONFIG_PATH at it.
COPY deploy/config.json /etc/cityfinder/config.json

COPY --from=builder /out/cityfinder /cityfinder
COPY --from=builder --chown=nonroot:nonroot /out/data /data

# Working directory for the (absolute) binary; also the resolution base for
# a relative CONFIG_PATH override.
WORKDIR /app
ENV PORT=3000 \
    CONFIG_PATH=/etc/cityfinder/config.json

EXPOSE 3000
USER nonroot
ENTRYPOINT ["/cityfinder"]

# HEALTHCHECK is deliberately absent. distroless/static-debian12:nonroot has
# no shell and no wget/curl, and /cityfinder has no healthcheck subcommand, so
# the only exec-form healthchecks would mean shipping a second binary into
# the image (busybox, a Go probe helper, ...) — rejected to keep the runtime
# surface minimal. Health is enforced where it belongs instead:
#   - Kubernetes: startup/readiness/liveness httpGet probes on /healthz, see
#     helm/city-finder/templates/deployment.yaml (the chart IS the healthcheck).
#   - Plain docker: curl http://localhost:<port>/healthz from the host.
