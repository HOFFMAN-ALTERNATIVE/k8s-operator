# 0002. Never adopt existing namespaces
Status: accepted
Date: 2026-10-17

## Context
A Tenant names a namespace. That namespace may already exist, created by a person or another tool, with workloads in it.

## Options
1. Adopt it: add labels, owner reference, quota and admins. Convenient for migrations. Puts someone else's workloads under a new quota and new admins, and with `deletionPolicy: Delete` deleting the Tenant would delete them.
2. Refuse: set `Ready=False/NamespaceConflict`, emit a Warning event, and never touch or delete that namespace.

## Decision
Option 2. An operator that can delete namespaces must be certain they are its own.

## Consequences
- Migrating an existing namespace needs an explicit step (label it, or create the Tenant with a new namespace name).
- envtest covers both halves: a conflicting namespace is not adopted, and deleting the conflicting Tenant leaves the namespace intact.
- Revisit with an explicit opt-in field (for example `adopt: true`) if migration demand appears.
