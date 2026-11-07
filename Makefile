# Copyright 2017 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

CMDS=iscsiplugin
all: build

include release-tools/build.make

GINKGO_FLAGS ?= -ginkgo.v
GO111MODULE = on
ifndef GOPATH
GOPATH ?= $(shell go env GOPATH)
endif
GOBIN ?= $(GOPATH)/bin
export GOPATH GOBIN GO111MODULE

REGISTRY ?= test
IMAGENAME ?= iscsi-csi
IMAGE_VERSION ?= latest
# Output type of docker buildx build
OUTPUT_TYPE ?= docker
ARCH ?= amd64
IMAGE_TAG = $(REGISTRY)/$(IMAGENAME):$(IMAGE_VERSION)
EXT_LDFLAGS = -s -w -extldflags "-static"

# Run all scenarios by default. Set to baseline or a comma-separated list of
# republish, driver-restart, target-restart, and shared-target to run fewer.
E2E_SCENARIOS ?= all
E2E_KIND_CLUSTER ?= kind

# Standalone example fixture commands require an explicitly selected namespace.
ISCSI_SERVER_NAMESPACE ?=

.EXPORT_ALL_VARIABLES:

all: iscsi

.PHONY: iscsi
iscsi:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -a -ldflags "${EXT_LDFLAGS}" -mod vendor -o bin/${ARCH}/iscsiplugin ./cmd/iscsiplugin

.PHONY: update
update:
	hack/update-dependencies.sh
	hack/verify-update.sh

.PHONY: test-container
test-container:
	make
	docker buildx build \
		--pull \
		--output=type=$(OUTPUT_TYPE) \
		--platform="linux/$(ARCH)" \
		--tag=$(IMAGE_TAG) \
		--build-arg=ARCH=$(ARCH) \
		.

.PHONY: sanity-test
sanity-test: iscsi
	./test/sanity/run-test.sh

.PHONY: unit-test
unit-test:
	mkdir -p bin
	go test -mod=vendor -v -race -coverprofile=bin/profile.cov ./pkg/...

.PHONY: verify
verify: unit-test
	./hack/verify-all.sh

.PHONY: mod-check
mod-check:
	go mod verify && [ "$(shell sha512sum go.mod)" = "`sha512sum go.mod`" ] || ( echo "ERROR: go.mod was modified by 'go mod verify'" && false )

.PHONY: clean
clean:
	go clean -mod=vendor -r -x
	rm -f bin/iscsiplugin

.PHONY: deploy-kind
deploy-kind:
	test/utils/deploy-kind.sh

# Check the code that sets up and runs the e2e tests without a cluster.
.PHONY: e2e-unit
e2e-unit:
	go test -mod=vendor -count=1 -v ./test/e2e/...

# Check the test's iSCSI server on its own, without Kubernetes or the driver.
.PHONY: e2e-fixture-test
e2e-fixture-test:
	go build -mod=vendor -o bin/e2e-fixture-test ./test/e2e/cmd/fixture-integration
	./bin/e2e-fixture-test

.PHONY: e2e-test
e2e-test: e2e-unit e2e-fixture-test
	go test -mod=vendor -tags=e2e -timeout=0 -v ./test/e2e $(GINKGO_FLAGS)

.PHONY: e2e-teardown
e2e-teardown:
	@if docker inspect "$(E2E_KIND_CLUSTER)-control-plane" >/dev/null 2>&1; then \
		sessions=$$(docker exec "$(E2E_KIND_CLUSTER)-control-plane" ls -A /sys/class/iscsi_session) || exit 1; \
		if [ -n "$$sessions" ]; then \
			echo 'Active iSCSI sessions remain; refusing to delete the cluster.' >&2; exit 1; \
		fi; \
		mounts=$$(docker exec "$(E2E_KIND_CLUSTER)-control-plane" awk '/\/volumes\/kubernetes.io~csi\/iscsi-pv-/ {print}' /proc/1/mountinfo) || exit 1; \
		if [ -n "$$mounts" ]; then \
			echo 'Test volume mounts remain; refusing to delete the cluster.' >&2; exit 1; \
		fi; \
	fi
	docker rm -f "$(E2E_KIND_CLUSTER)-iscsid" 2>/dev/null || true
	kind delete cluster --name "$(E2E_KIND_CLUSTER)"

.PHONY: install-iscsi-server
install-iscsi-server:
	@test -n "$(ISCSI_SERVER_NAMESPACE)" || (echo 'Set ISCSI_SERVER_NAMESPACE to a dedicated existing namespace' >&2; exit 2)
	kubectl create -f deploy/example/iscsi-server.yaml -n "$(ISCSI_SERVER_NAMESPACE)"
	kubectl rollout status deployment/iscsi-server -n "$(ISCSI_SERVER_NAMESPACE)" --timeout=120s

.PHONY: uninstall-iscsi-server
uninstall-iscsi-server:
	@test -n "$(ISCSI_SERVER_NAMESPACE)" || (echo 'Set ISCSI_SERVER_NAMESPACE to a dedicated existing namespace' >&2; exit 2)
	kubectl delete deployment,service -n "$(ISCSI_SERVER_NAMESPACE)" -l iscsi.csi.k8s.io/example=true --ignore-not-found
