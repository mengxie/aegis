# 3. Policy as code, with keys stored as hashes

## Status
Accepted

## Context
AI usage policy is a security and compliance control. Changes to it should be reviewed, versioned and reversible, rather than made by clicking in an admin console.

## Decision
- All policy lives in one JSON file kept in version control.
- Unknown fields are rejected, so a mistyped `"blockSecretss": false` cannot silently weaken a policy.
- All validation errors are reported at once.
- Provider secrets must be environment references (`${NAME}`), so the file is safe to commit.
- Team API keys are stored as SHA-256 hashes. `aegis new-key` shows a new key once and prints its config entry.

## Consequences
- A policy change is a pull request, with history and blame.
- Changing policy needs a restart or rolling deploy. Hot reload is a possible extension.
- A leaked config file reveals no usable credentials.
