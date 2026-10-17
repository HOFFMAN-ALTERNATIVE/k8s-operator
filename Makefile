# Tools are pinned and installed into ./bin on demand.
CONTROLLER_GEN_VERSION ?= v0.23.0
ENVTEST_K8S_VERSION    ?= 1.37.0
LOCALBIN               ?= $(CURDIR)/bin
CONTROLLER_GEN         ?= $(LOCALBIN)/controller-gen
SETUP_ENVTEST          ?= $(LOCALBIN)/setup-envtest
IMG                    ?= tenant-operator:dev

.PHONY: all generate manifests test lint build docker-build envtest e2e helm-lint verify
all: generate manifests lint test

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(CONTROLLER_GEN): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

$(SETUP_ENVTEST): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.25

generate: $(CONTROLLER_GEN) ## DeepCopy methods
	$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths=./api/...

manifests: $(CONTROLLER_GEN) ## CRD + RBAC from markers, synced into the Helm chart
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd paths=./... \
		output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac
	cp config/crd/bases/*.yaml charts/tenant-operator/crds/
	cp config/rbac/role.yaml charts/tenant-operator/files/manager-role.yaml

verify: generate manifests ## fail if generated files are stale
	git diff --exit-code -- api config charts

envtest: $(SETUP_ENVTEST)
	$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/k8s -p path

test: envtest ## unit + envtest integration tests
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN)/k8s -p path)" \
		go test -race -count=1 -coverprofile=cover.out ./...

lint:
	golangci-lint run ./...

build:
	CGO_ENABLED=0 go build -trimpath -o bin/manager ./cmd

docker-build:
	docker build -t $(IMG) .

helm-lint:
	helm lint charts/tenant-operator
	helm template t charts/tenant-operator | kubeconform -strict -summary -skip CustomResourceDefinition

e2e: ## needs a kind cluster: kind create cluster
	./test/e2e/e2e.sh kustomize
