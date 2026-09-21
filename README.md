# RSS Discovery

RSS aggregation and discovery API.

## Requirements

- Go 1.27+
- CGO enabled (DuckDB)
- A C/C++ toolchain (gcc/g++, libc/libstdc++ headers)

## Install

```sh
go install github.com/Quad4-Software/rss-discovery/cmd/rss-discovery@latest
```

Or clone and install from the tree:

```sh
git clone git@github.com:Quad4-Software/rss-discovery.git
cd rss-discovery
go install ./cmd/rss-discovery
```

## Build

```sh
go build -o bin/rss-discovery ./cmd/rss-discovery
```

```sh
make build
```

## Configure

```sh
cp config.example.toml config.toml
```

Edit config.toml as needed. Env overrides use the RSS_DISCOVERY_* prefix.

### Public mode

Set auth required off for anonymous readonly access:

```toml
[auth]
required = false
public_rate_per_min = 60
public_burst = 10
authed_reserved_concurrent = 1024
public_max_conn_per_ip = 8
```

Anonymous clients may GET readonly endpoints under the public IP rate limit. Token clients skip the public IP bucket, use their level rate limits, and keep reserved concurrency so public traffic is shed first under load. Writes and admin still need a token.

Or:

```sh
./bin/rss-discovery serve -config config.toml -auth-required=false -landlock=false
```

## Run

```sh
./bin/rss-discovery serve -config config.toml -landlock=false
```

```sh
make run
```

Default API listen address:

```
127.0.0.1:8787
```

### Make targets

```sh
make tidy          # go mod tidy
make build         # build bin/rss-discovery
make test          # unit tests
make race          # tests with -race
make fuzz          # security/auth/opml/api fuzzers
make bench         # benchmarks
make run           # build and serve
make seed          # import seed catalogs then exit
make fetch         # refresh 20 due feeds then exit
make token         # generate an admin API token
make docker-build  # build container image
make docker-up     # compose up with rebuild
```

Seed catalogs under `data/seeds/` include publisher feeds (`catalog.jsonl`), extras (HN front page, IndieBlog), and `hn-blogs.jsonl` (~1300 Hacker News personal blogs from the community OPML lists).

### CLI

```sh
rss-discovery serve [flags]
rss-discovery token generate --name NAME --level LEVEL [--ttl 720h]
rss-discovery token revoke ID
rss-discovery token list [--revoked]
rss-discovery token logs [--token-id ID] [--limit 50]
```

Levels:

```
readonly | standard | priority | admin
```

## Docker

```sh
podman build -t rss-discovery:local .
podman compose up -d --build
```

```sh
make docker-build
make docker-up
```

### Coolify

Use `docker-compose.coolify.yml` (not the local compose). Point Coolify at service `rss-discovery` port `8787` (domain like `https://rss.example.com:8787`).

Coolify builds from this repo's `Dockerfile`, or set `RSS_DISCOVERY_IMAGE` to a prebuilt image. A one-shot init chowns the data volume to UID `65532` (distroless nonroot).

Required (no port, no trailing slash — Coolify's `:8787` FQDN must not appear here):

```bash
RSS_DISCOVERY_PUBLIC_URL=https://rss.example.com
```

That value is used for WebSub `callback_url`. Config embeds `trust_proxy = true` for Traefik. Metrics listen on `127.0.0.1:8788` inside the container only.

Optional S3 (for example against hardened-stacks Garage):

```bash
RSS_DISCOVERY_S3_ENABLED=true
RSS_DISCOVERY_S3_ENDPOINT=http://garage:3900
RSS_DISCOVERY_S3_BUCKET=rss-discovery
RSS_DISCOVERY_S3_REGION=garage
RSS_DISCOVERY_S3_ACCESS_KEY=GKxxxxxxxx
RSS_DISCOVERY_S3_SECRET_KEY=...
```

Config already sets `force_path_style = true` for S3-compatible endpoints. Coolify HTTP healthcheck path: `/healthz` (send a `User-Agent` header; `require_user_agent` is on).

After first deploy, mint an admin token:

```sh
# exec into the container or run locally against the same volume
rss-discovery token generate --name coolify --level admin
```

## License

[0BSD License](LICENSE)
