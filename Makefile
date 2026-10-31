.PHONY: test race fuzz cover build demo
test:  ; go test ./...
race:  ; go test -race -count=1 ./...
fuzz:  ; go test ./internal/pii -run '^$$' -fuzz FuzzStreamRestorer -fuzztime 60s
cover: ; go test -coverpkg=./internal/... -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1
build: ; CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o aegis ./cmd/aegis
demo:  ; docker compose -f deploy/docker-compose.yml up --build --wait
