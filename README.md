# Aegis

**An AI governance gateway.** Aegis sits between a company's applications and its LLM providers, so teams can use AI without customer data leaving the company, without surprise bills, and with an audit trail.

[![CI](https://github.com/mengxie/aegis/actions/workflows/ci.yml/badge.svg)](https://github.com/mengxie/aegis/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-stdlib%20%2B%201%20dependency-00ADD8)
![License](https://img.shields.io/badge/license-MIT-blue)

## The problem

Every company adopting LLMs runs into the same four problems:

- **Data leaks.** Staff paste customer emails, phone numbers, card numbers and API keys into prompts, and those prompts go to a third party.
- **Invisible spend.** Nobody knows which team spent what until the invoice arrives.
- **No audit trail.** When a regulator or customer asks "what did you send to the AI?", there's no answer.
- **Fragile integrations.** Every team wires up providers differently, and a provider outage takes every feature down.

## What Aegis does

Applications keep the OpenAI SDK they already use and change one line:

```python
client = OpenAI(base_url="https://aegis.internal.acme.co.nz/v1", api_key="aeg_...")
```

Then, for every request, Aegis applies the policy for that team:

| | |
|---|---|
| **Reversible PII protection** | Emails, phone numbers, card numbers, IBANs and IP addresses are swapped for placeholders before the prompt leaves the company, and restored in the response, **including token-by-token streams**. |
| **Secret blocking** | Prompts containing AWS keys, GitHub and Slack tokens, OpenAI/Anthropic keys, JWTs or private keys are rejected outright. |
| **Policy as code** | Allowed models, token caps, PII mode and blocked terms (such as confidential project code names) live in a version-controlled config file and are reviewed like code. |
| **Budgets and rate limits** | Monthly spend budgets per team, exact to the nano-dollar, and per-key request limits. |
| **Failover** | Each model alias has an ordered list of providers, such as Azure in Australia East, then OpenAI. Circuit breakers stop sending traffic to a failing provider. |
| **Caching** | Identical deterministic requests (temperature 0) are answered from cache, so a repeat costs nothing. |
| **Audit and observability** | Every request is logged with team, model, tokens, cost, PII counts and decisions, but **never the prompt text**. Prometheus metrics, structured JSON logs and request IDs are built in. |

Errors use OpenAI's error format, so existing apps see a policy decision as an ordinary `PermissionDeniedError`.

## See it work

This is real output from the official **OpenAI Python SDK** talking to Aegis, backed by PostgreSQL. The app sends:

> Draft a refund reply to Aroha (aroha.ngata@acme.co.nz, +64 21 555 0199), card 4111 1111 1111 1111.

**The provider receives:**

> Draft a refund reply to Aroha ([EMAIL_1], [PHONE_1]), card [CARD_1].

**The app gets back** the real email, phone number and card number, restored, including in streamed responses where a placeholder arrives split across chunks (`[EMA` + `IL_1]`).

The rest of the run:

```
4. secret       -> PermissionDeniedError 403 secret_detected
5. blocked term -> PermissionDeniedError 403 blocked_term
6. pii=block    -> PermissionDeniedError 403 pii_detected
7. model policy -> PermissionDeniedError 403 model_not_allowed
8. bad key      -> AuthenticationError 401
9. cache (temperature 0): ['miss', 'hit']
```

The audit table records each request; there is no column for prompt text:

```
   team    |    model    | status | p  | c  | nano_usd | cached |                 pii                 |    decision
-----------+-------------+--------+----+----+----------+--------+-------------------------------------+-----------------
 support   | gpt-4o-mini |    200 | 11 | 11 |     8250 | f      | {"CARD": 1, "EMAIL": 1, "PHONE": 1} | allowed
 support   | gpt-4o-mini |    403 |  0 |  0 |        0 | f      | {}                                  | secret_detected
 analytics | gpt-4o-mini |    403 |  0 |  0 |        0 | f      | {"CARD": 1, "EMAIL": 1, "PHONE": 1} | pii_detected
```

## Quick start

You need Docker. The stack includes a mock LLM provider, so no API keys are needed.

```bash
docker compose -f deploy/docker-compose.yml up --build --wait
```

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer aeg_demo_support" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Email aroha@acme.co.nz"}]}'

docker compose -f deploy/docker-compose.yml logs mock   # what actually left the company
```

`aeg_demo_support` and `aeg_demo_analytics` are demo keys for this local stack only. To connect real providers, start from [`deploy/aegis.production.example.json`](deploy/aegis.production.example.json).

## Configuration

One JSON file, reviewed in pull requests. Unknown fields are a startup error, so a typo in a policy cannot be silently ignored, and every problem is reported at once.

```jsonc
{
  "upstreams": {
    "azure-nz": { "type": "azure-openai", "baseURL": "https://...", "apiKey": "${AZURE_OPENAI_API_KEY}", "apiVersion": "2024-10-21" },
    "openai":   { "type": "openai", "baseURL": "https://api.openai.com/v1", "apiKey": "${OPENAI_API_KEY}" }
  },
  "models": {
    "gpt-4o": {
      "routes": [{ "upstream": "azure-nz", "model": "gpt-4o-prod" }, { "upstream": "openai", "model": "gpt-4o" }],
      "inputPerMTok": 2.5, "outputPerMTok": 10
    }
  },
  "teams": {
    "customer-support": {
      "keys": ["sha256:..."],
      "policy": { "models": ["gpt-4o"], "pii": "pseudonymize", "maxTokens": 2048,
                  "requestsPerMinute": 600, "monthlyBudgetUSD": 2000, "cacheSeconds": 600,
                  "blockedTerms": ["Project Orca"] }
    }
  }
}
```

- **Upstream types.** `openai` covers OpenAI and any compatible API (vLLM, Ollama, Mistral, Groq and others); `azure-openai` covers Azure.
- **Secrets** must be environment references (`"${NAME}"`). A key pasted directly into the file is rejected.
- **API keys** are stored only as SHA-256 hashes. `aegis new-key` prints a new key once, together with its config entry.

| `pii` mode | What the provider sees | What the app gets back |
|---|---|---|
| `pseudonymize` (default) | `[EMAIL_1]` | The original value |
| `redact` | `[EMAIL]` | `[EMAIL]` |
| `block` | Nothing: the request is rejected with 403 | |
| `allow` | The original value (for approved, contracted providers) | |

`blockSecrets` defaults to `true` in every mode.

## Operations

- **Endpoints.** `POST /v1/chat/completions` and `GET /v1/models` (OpenAI-compatible), plus `/healthz`, `/readyz` (which checks the database) and `/metrics` (Prometheus).
- **Metrics.** `aegis_requests_total`, `aegis_tokens_total`, `aegis_cost_usd_total`, `aegis_pii_protected_total`, `aegis_upstream_failures_total`, `aegis_cache_hits_total` and `aegis_request_duration_seconds`.
- **Response headers.** `X-Aegis-Request-Id` (honours an incoming `X-Request-Id`), `X-Aegis-Upstream`, `X-Aegis-Cache` and `X-Aegis-Pii-Protected`.
- **Scaling.** Gateway replicas are stateless. Budgets and the audit log are shared through PostgreSQL, with an atomic upsert so concurrent replicas never lose spend. [`deploy/kubernetes/aegis.yaml`](deploy/kubernetes/aegis.yaml) runs 3–20 replicas spread across zones, with a pod disruption budget, a read-only root filesystem and no capabilities.
- **Shutdown.** On SIGTERM, in-flight requests and streams get 30 seconds to finish.
- **Image.** A distroless, non-root image of about 10 MB, containing a single static binary.

## Architecture

```
cmd/aegis            serve | new-key | mock-upstream | healthcheck
internal/gateway     HTTP pipeline: auth → rate limit → policy → budget → PII → cache → route → audit
internal/pii         detectors with checksums, reversible vault, streaming restorer
internal/policy      per-team rules
internal/provider    OpenAI and Azure clients, circuit breakers
internal/usage       integer nano-dollar pricing; PostgreSQL and in-memory ledgers
internal/config      validated policy-as-code
internal/ratelimit   token buckets
internal/cache       LRU with expiry
internal/metrics     Prometheus text format
internal/mock        OpenAI-compatible stand-in provider
```

Key decisions are written up in [`docs/adr`](docs/adr).

## Engineering

- **One dependency.** Everything except the PostgreSQL driver is the Go standard library, which keeps the supply-chain surface small for a component that sees every prompt.
- **The race detector runs on every test** in CI, including the concurrent ledger test, where 25 parallel writes must add up exactly.
- **Fuzzing.** A native Go fuzz test proves that streaming never changes the result: however a response is cut into chunks, the restored stream equals restoring the whole text. CI fuzzes it on every push.
- **End-to-end tests** run the real gateway over HTTP against mock providers, covering every policy path, failover, circuit breaking, caching and budget enforcement. 87.8% statement coverage across packages.
- **Container test.** CI builds the image, starts the Compose stack, and checks from outside the container that personal data never reaches the provider's logs.
- **Supply chain.** `govulncheck` on every push, and Dependabot for Go modules, Actions and base images.

## Limits, stated plainly

- **PII detection is pattern-based**, with checksums for cards (Luhn) and IBANs (mod-97). It doesn't find names or street addresses: in the example above, "Aroha" reaches the provider. A named-entity detector (for example, a Microsoft Presidio sidecar) is the natural next detector.
- **Budgets are soft** by the cost of requests already in flight: a request is allowed if the team is under budget when it starts.
- **Rate limits are per replica.** With three replicas, a key gets up to three times its limit. A shared limiter in PostgreSQL or Redis is on the roadmap.
- **Chat completions only** for now. Embeddings and the Responses API are next.

## Pairs with

[**Witness**](https://github.com/mengxie/witness): forward Aegis's audit records to Witness to make the AI usage log tamper-evident.

## Licence

MIT
