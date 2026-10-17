#!/usr/bin/env bash
# End-to-end test on a real kind cluster (real namespace controller, real
# garbage collector, real Pod Security Admission):
#   install the operator (kustomize or helm) -> create a Tenant -> verify
#   namespace, quota, LimitRange defaulting, NetworkPolicies, RBAC, PSA ->
#   tamper with the quota (drift is reverted) -> delete (namespace is really
#   gone) -> Retain policy (namespace survives, orphaned).
set -euo pipefail
cd "$(dirname "$0")/../.."

METHOD=${1:-kustomize}
CLUSTER=${KIND_CLUSTER:-kind}
IMG=tenant-operator:e2e
NS=tenant-operator-system

log() { printf '\033[36m[e2e]\033[0m %s\n' "$*"; }
dump() {
  log "--- operator logs ---"
  kubectl -n "$NS" logs -l app.kubernetes.io/name=tenant-operator --tail=100 || true
  kubectl get tenants -o wide || true
}
fail() { printf '\033[31m[e2e] FAIL:\033[0m %s\n' "$*"; dump; exit 1; }
trap 'dump' ERR

wait_for() { # description, command...
  local what=$1; shift
  for _ in $(seq 1 60); do "$@" >/dev/null 2>&1 && return 0; sleep 2; done
  fail "timed out waiting for: $what"
}

log "building and loading $IMG into kind/$CLUSTER"
docker build -q -t "$IMG" .
kind load docker-image "$IMG" --name "$CLUSTER"

case "$METHOD" in
  kustomize)
    kubectl apply --server-side -k test/e2e/overlay
    DEPLOY=tenant-operator-controller-manager ;;
  helm)
    helm upgrade --install tenant-operator charts/tenant-operator -n "$NS" --create-namespace \
      --set image.repository=tenant-operator --set image.tag=e2e --wait --timeout 3m
    DEPLOY=tenant-operator ;;
  *) fail "unknown install method $METHOD" ;;
esac
kubectl -n "$NS" rollout status "deploy/$DEPLOY" --timeout=180s

log "creating Tenant team-a"
kubectl apply -f config/samples/tenancy_v1alpha1_tenant.yaml
kubectl wait tenant/team-a --for=condition=Ready --timeout=90s
kubectl get tenants

[[ $(kubectl get ns team-a -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}') == restricted ]] || fail "PSA label missing"
kubectl -n team-a get resourcequota tenant-quota limitrange/tenant-defaults rolebinding/tenant-admins >/dev/null
[[ $(kubectl -n team-a get networkpolicy -o name | wc -l) -eq 2 ]] || fail "expected 2 NetworkPolicies"

log "RBAC: group team-a-admins is admin in team-a and nowhere else"
[[ $(kubectl auth can-i create deployments -n team-a --as=alice --as-group=team-a-admins) == yes ]] || fail "admin group cannot deploy"
[[ $(kubectl auth can-i create deployments -n default --as=alice --as-group=team-a-admins || true) == no ]] || fail "admin group leaked outside its namespace"
[[ $(kubectl auth can-i create deployments -n team-a --as=mallory || true) == no ]] || fail "outsider can deploy"

log "Pod Security Admission rejects a privileged pod"
if out=$(kubectl -n team-a run priv --image=busybox --privileged --dry-run=server 2>&1); then
  fail "privileged pod admitted: $out"
fi
grep -q "violates PodSecurity" <<<"$out" || fail "unexpected rejection: $out"

log "LimitRange injects default requests into a compliant pod"
cpu=$(kubectl -n team-a apply --dry-run=server -o jsonpath='{.spec.containers[0].resources.requests.cpu}' -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata: {name: ok}
spec:
  securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: c
      image: busybox
      securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}}
YAML
)
[[ $cpu == 100m ]] || fail "default cpu request = '$cpu', want 100m"

log "drift: hand-edit the quota, operator reverts it"
kubectl -n team-a patch resourcequota tenant-quota --type merge -p '{"spec":{"hard":{"pods":"999"}}}'
quota_restored() { [[ $(kubectl -n team-a get resourcequota tenant-quota -o jsonpath='{.spec.hard.pods}') == 50 ]]; }
wait_for "quota drift reverted" quota_restored

log "delete (policy Delete): namespace must be gone before the Tenant is"
kubectl delete tenant team-a --wait=false
kubectl wait --for=delete ns/team-a --timeout=180s
kubectl wait --for=delete tenant/team-a --timeout=60s

log "delete (policy Retain): namespace survives, orphaned"
kubectl apply -f - <<'YAML'
apiVersion: tenancy.hoffman-alternative.io/v1alpha1
kind: Tenant
metadata: {name: team-b}
spec: {deletionPolicy: Retain, quota: {pods: "5"}}
YAML
kubectl wait tenant/team-b --for=condition=Ready --timeout=90s
kubectl delete tenant team-b --timeout=60s
sleep 5 # give the garbage collector a chance to (wrongly) act
[[ $(kubectl get ns team-b -o jsonpath='{.status.phase}') == Active ]] || fail "retained namespace not Active"
[[ -z $(kubectl get ns team-b -o jsonpath='{.metadata.ownerReferences}') ]] || fail "retained namespace still has owners"

log "OK ($METHOD)"
