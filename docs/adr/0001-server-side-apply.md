# 0001. Server-side apply instead of CreateOrUpdate
Status: accepted
Date: 2026-10-17

## Context
The reconciler owns five object kinds per tenant and must correct drift when someone edits them. The usual controller-runtime helper, `CreateOrUpdate`, does get, mutate, update.

## Options
1. `CreateOrUpdate`. Familiar. Read-modify-write races, `resourceVersion` conflicts under concurrent edits, and no record of which fields the operator owns, so it either clobbers other controllers' fields or misses drift.
2. Server-side apply with typed apply configurations and a fixed field manager (`tenant-operator`), `ForceOwnership` on. The API server tracks field ownership; one request per object, no conflicts to retry.

## Decision
Option 2. Drift correction falls out of it: the operator re-applies the fields it declares and wins, while fields other actors own are left alone.

## Consequences
- Each reconcile recomputes the full desired state, so missed or coalesced events cannot leave a tenant half-provisioned.
- Requires Kubernetes 1.22+ (SSA GA). Not a constraint in practice.
- `ForceOwnership` means a human edit to an operator-owned field is always reverted. That is the intent, and it is documented in the README.
