# Deployment

The service ships as a container image and a Helm chart. This repository
contains nothing that talks to a cluster. Registering the image (e.g. Harbor)
and the GitOps `Application` (e.g. ArgoCD; the chart's manifests already carry
`argocd.argoproj.io/sync-wave` annotations) is an operations handoff.

## Image

Built from the repo-root `Dockerfile`: a `golang:1.26` builder, then a
`gcr.io/distroless/static-debian12:nonroot` runtime (static binary, non-root
uid 65532, no shell, no package manager). Pushed `v*` tags publish
`ghcr.io/<owner>/city-finder:<version>` (owner lowercased) for `linux/amd64`
and `linux/arm64` (`.github/workflows/release.yml`). Plain semver tags also
move `latest`.

```bash
docker build -t city-finder .
docker volume create city-finder-data
docker run -d --name city-finder -p 3000:3000 \
  -v city-finder-data:/data --memory 14g city-finder
curl http://localhost:3000/healthz
```

| Variable | Default in image | Purpose |
|---|---|---|
| `PORT` | `3000` | Listen port |
| `CONFIG_PATH` | `/etc/cityfinder/config.json` | Shipped config (`deploy/config.json`): the repo config with `datasets_folder` pinned to `/data/datasets`. To override, mount your own file and point `CONFIG_PATH` at it. A relative path resolves against `/app`. |

Other runtime variables (`PPROF_ADDR`, `GOMAXPROCS`, `GOMEMLIMIT`) are in
[configuration.md](configuration.md#runtime-environment).

## Datasets volume (`/data`)

Everything the server downloads and builds lives under `/data/datasets`.
Options, from slowest to fastest boot:

1. **Empty volume.** The first start downloads ~420 MB of GeoNames archives
   (plus the optional ~120 KB admin1-names file) and builds all indexes: about
   6–8 minutes and a ~9 GB RSS peak. Fine for a trial.
2. **Raw datasets mounted.** Pre-place the extracted dumps; boot skips the
   download but still builds the indexes.
3. **Pre-built indexes mounted (recommended).** `s2index.gob`,
   `name_index.gob`, `postal_code_index.gob`: a warm start in ~20–25 s.

```bash
# Seed a named volume from a local datasets/ directory with built indexes
docker run --rm -v city-finder-data:/data -v "$PWD/datasets:/seed:ro" \
  alpine sh -c 'mkdir -p /data/datasets && cp /seed/*.gob /data/datasets/'
```

Build indexes locally with `make build-prod` (full dump, several minutes) or
`make build-test` (test fixture).

How initialization behaves:

- **Downloads** are verified (HTTP status), streamed to a temp file and
  renamed atomically. Extraction is atomic too, so a crash cannot leave a
  truncated dataset for a later boot to bake into the indexes. An archive that
  fails to extract is re-downloaded once. Archives are deleted after
  extraction.
- **Index presence:** when all three indexes exist, the raw datasets are not
  downloaded at all and the indexes decode concurrently. A missing index is
  rebuilt on its own, fetching the datasets on demand.
- **Old formats:** index files from older format versions are rejected and
  rebuilt once, automatically.
- **Concurrent boots:** a lock file allows one initializer per datasets
  folder, so two cold-booting pods sharing a volume cannot truncate each
  other's writes.

## Helm chart

`helm/city-finder`: a Deployment with rolling updates, a Service, an optional
PVC and Ingress, an optional PodDisruptionBudget, and probes on `GET /healthz`.

```bash
helm install city-finder ./helm/city-finder \
  --namespace city-finder --create-namespace \
  --set image.repository=ghcr.io/<owner>/city-finder \
  --set persistence.enabled=true
```

- **Image tag** defaults to the chart's `appVersion`, which CI keeps equal to
  the latest release in `CHANGELOG.md`. The release job refuses a tag that
  does not match.
- **Memory:** request `10Gi`, limit `14Gi` (see
  [performance.md](performance.md#memory-sizing)). The startup-probe budget is
  600 s, which covers a cold start. Re-check both whenever the index format
  changes.
- **CPU:** request `500m` and no limit, to avoid CFS throttling during the
  index build and GC. Without a limit the Go runtime sizes `GOMAXPROCS` to the
  node; pin it through `server.extraEnv` for reproducible behaviour across
  node types.
- **Rollout:** a single replica rolls without an outage window
  (`maxUnavailable: 0`, `maxSurge: 1`). Scale to two or more replicas before
  enabling the default-off PodDisruptionBudget.
- **Profiling:** set `PPROF_ADDR=127.0.0.1:6060` via `server.extraEnv` and
  `kubectl port-forward` to it.
- **Validation:** CI runs `helm lint` and `kubeconform -strict` on the default
  and the full-feature value permutations. Validate local changes the same way.
