# 0003. Validation in the API server with CEL, no webhooks
Status: accepted
Date: 2026-10-17

## Context
Bad Tenant specs (invalid namespace names, changing `spec.namespace` after creation, unbounded quota maps) should be rejected at `kubectl apply`, not discovered in operator logs.

## Options
1. Validating and defaulting webhooks. Arbitrary logic. Needs serving certificates, a highly available webhook Deployment, and a failure policy; a down webhook blocks every Tenant write.
2. OpenAPI schema plus CEL rules in the CRD (enums, DNS-label pattern, `maxProperties`, defaults, transition rule for immutability). Evaluated by the API server itself; nothing extra to run.

## Decision
Option 2. Every rule this API needs is expressible in CEL, and it removes a component that can take Tenant writes down.

## Consequences
- Maps are bounded (`maxProperties`) to stay within the CEL cost budget.
- envtest asserts the rules against a real API server.
- A conversion webhook is still needed once `v1beta1` exists; that is the point to reconsider webhooks generally.
