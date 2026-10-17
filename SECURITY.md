# Security

## Reporting

Report vulnerabilities privately through GitHub's "Report a vulnerability" button on this repository (Security tab). I aim to reply within 72 hours. Please do not open public issues for exploitable bugs.

## Threat model

- **Assets:** tenant isolation (namespaces, quotas, network policies, RBAC); the operator's own cluster permissions.
- **Actors:** tenant admins trying to escape their namespace; anyone allowed to create Tenant objects; a compromised operator pod.
- **Entry points:** the Tenant API (cluster-scoped), the metrics endpoint, the operator's ServiceAccount token.
- **Top risks and mitigations:**
  - Privilege escalation through the operator: RBAC generated from markers; binding `admin` uses the `bind` verb limited to `resourceNames: [admin]` rather than granting the operator everything in `admin`.
  - Taking over namespaces that are not the tenant's: the operator never adopts an existing namespace (ADR 0002).
  - Weakening isolation by hand edits: server-side apply with forced ownership reverts drift on managed objects.
  - Metrics leakage: served over HTTPS behind TokenReview/SubjectAccessReview; HTTP/2 disabled on that endpoint.
  - Container compromise: distroless, non-root (65532), read-only root filesystem, all capabilities dropped, `RuntimeDefault` seccomp.

## Who may create Tenants

Creating a Tenant is equivalent to creating a namespace and granting admin in it. Restrict `create` on `tenants.tenancy.hoffman-alternative.io` to platform administrators or your GitOps controller.
