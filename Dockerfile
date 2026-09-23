# Multi-stage, rootless, digest-pinned image for rss-discovery.
# DuckDB requires CGO + libstdc++, so runtime is distroless/cc (not static).
# Digests resolved 2026-09-20 from registry content digests (OCI index).

# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=docker.io/library/golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195
ARG RUNTIME_IMAGE=gcr.io/distroless/cc-debian12:nonroot@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f

# ----- build -----
FROM ${GO_IMAGE} AS build
WORKDIR /src

ENV CGO_ENABLED=1 \
    GOOS=linux \
    GOFLAGS="-trimpath" \
    GOTOOLCHAIN=local

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      gcc \
      g++ \
      libc6-dev \
      libstdc++-12-dev \
      pkg-config \
 && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w -buildid=" -o /out/rss-discovery ./cmd/rss-discovery \
 && go version -m /out/rss-discovery >/out/buildinfo.txt

# ----- runtime (distroless, nonroot UID 65532) -----
FROM ${RUNTIME_IMAGE}

# OCI labels. CI overrides these from git metadata at build time.
ARG IMAGE_TITLE=rss-discovery
ARG IMAGE_DESCRIPTION=RSS aggregation and discovery API
ARG IMAGE_SOURCE=https://github.com/Quad4-Software/rss-discovery
ARG IMAGE_URL=https://github.com/Quad4-Software/rss-discovery
ARG IMAGE_DOCUMENTATION=https://github.com/Quad4-Software/rss-discovery
ARG IMAGE_VENDOR=Quad4 Software
ARG IMAGE_LICENSES=0BSD
ARG IMAGE_REVISION=unknown
ARG IMAGE_VERSION=dev
ARG IMAGE_CREATED=

LABEL org.opencontainers.image.title="${IMAGE_TITLE}" \
      org.opencontainers.image.description="${IMAGE_DESCRIPTION}" \
      org.opencontainers.image.source="${IMAGE_SOURCE}" \
      org.opencontainers.image.url="${IMAGE_URL}" \
      org.opencontainers.image.documentation="${IMAGE_DOCUMENTATION}" \
      org.opencontainers.image.vendor="${IMAGE_VENDOR}" \
      org.opencontainers.image.licenses="${IMAGE_LICENSES}" \
      org.opencontainers.image.revision="${IMAGE_REVISION}" \
      org.opencontainers.image.version="${IMAGE_VERSION}" \
      org.opencontainers.image.created="${IMAGE_CREATED}"

WORKDIR /app

COPY --from=build --chown=65532:65532 /out/rss-discovery /app/rss-discovery
COPY --from=build --chown=65532:65532 /src/config.example.toml /app/config.example.toml
COPY --from=build --chown=65532:65532 /src/data/blocklist.txt /app/data/blocklist.txt
COPY --from=build --chown=65532:65532 /src/data/seeds /app/seeds

ENV HOME=/data \
    XDG_CACHE_HOME=/tmp \
    RSS_DISCOVERY_DATA_DIR=/data \
    RSS_DISCOVERY_DB_PATH=/data/rss.duckdb \
    RSS_DISCOVERY_SEED_DIR=/data/seeds \
    RSS_DISCOVERY_SERVER_ADDR=0.0.0.0:8787 \
    RSS_DISCOVERY_METRICS_ADDR=0.0.0.0:8788 \
    RSS_DISCOVERY_LANDLOCK=false

USER 65532:65532

EXPOSE 8787 8788

VOLUME ["/data"]

ENTRYPOINT ["/app/rss-discovery"]
CMD ["serve", "-config", "/config/config.toml", "-landlock=false"]
