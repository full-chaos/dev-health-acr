# STORAGE INTERFACES AND ADAPTERS

## OVERVIEW

Defines Principal-scoped evidence, packet, episode, credential, and audit interfaces, with memory and Postgres implementations. Callers depend on interface behavior, not adapter-specific details.

## WHERE TO LOOK

| Task | Location | Notes |
| --- | --- | --- |
| Shared contracts | `interfaces.go` | `Principal`, records, sentinel errors, store interfaces |
| Test/dev adapter | `memory/` | Mutex-protected maps and defensive copies |
| Production adapter | `postgres/` | SQL transactions, conflict mapping, retention queries |
| Database shape | `migrations/postgres/` | Schema is outside this package tree |

## INTERFACE INVARIANTS

- Repository-scoped methods receive a validated `Principal`; enforce org and repository scope inside the adapter operation.
- Foreign, expired, deleted, unauthorized, and unknown opaque objects use non-enumerating not-found behavior where the public contract requires it.
- `CredentialRecord.TokenHash` is server-only. Public DTOs, logs, and audit metadata never contain it.
- Packet and episode operations preserve idempotency/conflict distinctions defined by the interface.
- Memory returns defensive copies of mutable slices/maps/JSON so callers cannot mutate stored state.
- Memory and Postgres must return equivalent sentinel errors and observable results for the same inputs.

## POSTGRES RULES

- Constructors accept caller-owned `*sql.DB`; adapters do not parse DSNs, open drivers, run migrations, or own process-level pooling.
- Multi-record lifecycle operations use transactions when atomicity requires them, especially rotation and idempotent creation.
- Convert driver-specific no-row/constraint outcomes to storage sentinels at the adapter boundary.
- Keep SQL parameterized and organization/repository predicates explicit.

## TESTING

- Memory tests lock cloning, scope, expiry, idempotency, and conflicts.
- Postgres driver tests lock query/transaction behavior without moving driver concerns into interfaces.
- Access tests must include cross-org and cross-repository attempts.
- Retention tests distinguish expiry visibility from purge mechanics.
- Run `go test ./internal/storage/...`; adapter changes should also run `go test -race ./internal/storage/...`.

## ANTI-PATTERNS

- Do not infer Principal or scope inside adapters from payload fields.
- Do not leak existence through adapter-specific authorization errors.
- Do not expose raw SQL/driver errors across the storage interface.
- Do not add behavior to one adapter without parity coverage for the other.
