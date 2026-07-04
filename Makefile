.PHONY: build run test test-race integration verify web package package-web docker

build:
	go build -buildvcs=false -o bin/gpt2api-image ./cmd/server

run:
	GPT2API_IMAGE_ADDR=:3000 go run ./cmd/server

test:
	go test ./cmd/... ./internal/...

test-race:
	go test -race ./internal/app

integration:
	bash scripts/run_pg_integration.sh

verify: test test-race
	go vet ./cmd/... ./internal/...

web:
	cd web && pnpm install --frozen-lockfile && pnpm run build
	rm -rf web_dist && cp -R web/out web_dist

package:
	scripts/package_release.sh

package-web:
	scripts/package_release.sh --web

docker:
	docker build -t gpt2api-image .
