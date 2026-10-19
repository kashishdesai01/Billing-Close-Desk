export TEST_DATABASE_URL

.PHONY: build test verify demo e2e up down
build:
	npm --prefix web ci
	npm --prefix web run build
	go build -trimpath -o bin/desk ./cmd/desk
	go build -trimpath -o bin/erpsim ./cmd/erpsim
test:
	@test -n "$$TEST_DATABASE_URL" || (echo "TEST_DATABASE_URL is required for full verification" && exit 1)
	go vet ./...
	go test -race ./... -count=1
	npm --prefix web run format:check
	npm --prefix web test
	npm --prefix web run build
verify:
	go run ./cmd/desk -mode verify
e2e:
	python3 scripts/e2e.py
demo:
	python3 scripts/demo.py
up:
	docker compose up --build -d
down:
	docker compose down
