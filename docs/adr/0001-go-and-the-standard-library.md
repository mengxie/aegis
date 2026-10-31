# 1. Go, and almost only the standard library

## Status
Accepted

## Context
A gateway is on the path of every AI request in a company. It must add little latency, handle many concurrent streams, start fast and be easy to operate. It also sees every prompt, so each third-party dependency is part of the attack surface.

## Decision
Write Aegis in Go and use the standard library for HTTP, streaming, JSON, structured logging (`log/slog`), metrics output and tests. The only dependency is the PostgreSQL driver `lib/pq`.

## Consequences
- The binary is static and 8 MB, so the image can be distroless and non-root.
- Goroutines make each streamed response cheap.
- There are fewer conveniences: Prometheus metrics are written by hand (about 150 lines), and the config is JSON because YAML would need a dependency.
- `govulncheck` mostly has the Go toolchain itself to check.
