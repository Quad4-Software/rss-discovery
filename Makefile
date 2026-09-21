.PHONY: all build test tidy run seed fetch race leak token bench fuzz docker-build docker-up

all: tidy test build

tidy:
	go mod tidy

build:
	go build -o bin/rss-discovery ./cmd/rss-discovery

test:
	go test ./...

bench:
	go test -bench=. -benchmem ./internal/httpx ./internal/parse ./internal/security

fuzz:
	go test ./internal/security -run=^$$ -fuzz=FuzzValidatePublicHTTPSURL -fuzztime=10s
	go test ./internal/security -run=^$$ -fuzz=FuzzIsProbePath -fuzztime=5s
	go test ./internal/security -run=^$$ -fuzz=FuzzIsScannerUA -fuzztime=5s
	go test ./internal/security -run=^$$ -fuzz=FuzzIsPublicIP -fuzztime=5s
	go test ./internal/auth -run=^$$ -fuzz=FuzzParseLevel -fuzztime=5s
	go test ./internal/auth -run=^$$ -fuzz=FuzzHashToken -fuzztime=5s
	go test ./internal/auth -run=^$$ -fuzz=FuzzAllows -fuzztime=5s
	go test ./internal/opml -run=^$$ -fuzz=FuzzParse -fuzztime=5s
	go test ./internal/api -run=^$$ -fuzz=FuzzAPIPaths -fuzztime=10s

race:
	go test -race ./...

leak:
	go test ./internal/api -count=1

run: build
	./bin/rss-discovery serve -config config.toml -landlock=false

seed: build
	./bin/rss-discovery serve -config config.toml -landlock=false -seed -seed-exit

fetch: build
	./bin/rss-discovery serve -config config.toml -landlock=false -fetch 20

token: build
	./bin/rss-discovery token generate --name admin --level admin

docker-build:
	podman build -t rss-discovery:local .

docker-up:
	podman compose up -d --build
