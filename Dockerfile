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

# lib/config.LoadConfig resolves every relative path against a "project root"
# it discovers by walking up from the CWD to a go.mod file, and fails outright
# when none exists. The runtime image therefore ships the module file as a
# root marker next to WORKDIR, and every path the server consumes is
# absolute: CONFIG_PATH=/etc/cityfinder/config.json (below) and
# datasets_folder=/data/datasets (deploy/config.json). Removing this marker
# requires a source change (config loading must not depend on project-root
# discovery) — owned outside the deploy lane; flagged in the lane report.
COPY --from=builder /src/go.mod /app/go.mod

# Default configuration: absolute paths only, datasets under the /data volume.
# Override by mounting your own file and pointing CONFIG_PATH at it.
COPY deploy/config.json /etc/cityfinder/config.json

COPY --from=builder /out/cityfinder /cityfinder
COPY --from=builder --chown=nonroot:nonroot /out/data /data

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
