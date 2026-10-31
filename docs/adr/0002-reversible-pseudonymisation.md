# 2. Reversible pseudonymisation, restored while streaming

## Status
Accepted

## Context
Redacting personal data (`[EMAIL]`) protects it but breaks the task. A support agent asking for "a reply to this customer" gets back a reply addressed to `[EMAIL]`. Blocking is safer still, but stops the work entirely.

## Decision
By default, each value is replaced with a numbered placeholder (`[EMAIL_1]`), using a per-request vault, and the original is restored in the response. The same value always gets the same placeholder, so the model can still reason ("both messages came from the same customer").

Streaming is the hard part: a placeholder can arrive split across chunks (`[EMA` + `IL_1]`). The `StreamRestorer` holds back only a trailing fragment that could still become a placeholder. Placeholders contain no `[` after their first character, so only the last `[` in the buffer can start one, and the delay is at most 15 characters.

## Consequences
- The property "streaming gives the same result as restoring the whole text" is tested with Go's native fuzzer in CI.
- The vault lives only in memory for the duration of one request and is never stored.
- Placeholders the model invents (`[EMAIL_7]` when only one email exists) are left as they are.
- Teams that cannot accept any personal data leaving use `redact` or `block`.
