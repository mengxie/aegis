# 4. Integer nano-dollar accounting and soft budgets

## Status
Accepted

## Context
LLM requests cost tiny, fractional amounts: one token of gpt-4o-mini input is $0.00000015. Summing millions of float charges drifts. Budgets must also hold across several gateway replicas.

## Decision
- Money is an `int64` count of nano-dollars. Conveniently, $1 per million tokens is exactly 1,000 nano-dollars per token. A test adds a million $0.00000015 charges and requires exactly $0.15.
- The spend ledger is a PostgreSQL row per team and month, increased with `INSERT … ON CONFLICT DO UPDATE … RETURNING`. That is atomic, so concurrent replicas never lose an increment (tested with 25 parallel writes).
- A request is allowed if the team is under budget when it starts.

## Consequences
- Budgets are soft by the cost of requests already in flight. A hard limit would mean reserving an estimated cost before each call and reconciling afterwards; the record format allows that later.
- Prices live in config next to each model, so a price change is a reviewed change.
