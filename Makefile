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

GOPATH ?= $(shell go env GOPATH)
GOBIN ?= $(GOPATH)/bin
export GOPATH GOBIN

REGISTRY ?= test
IMAGENAME ?= iscsi-csi
# Output type of docker buildx build
OUTPUT_TYPE ?= docker
ARCH ?= amd64
IMAGE_TAG = $(REGISTRY)/$(IMAGENAME):$(IMAGE_VERSION)

.PHONY: test-container
test-container:
	make
	docker buildx build --pull --output=type=$(OUTPUT_TYPE) --platform="linux/$(ARCH)" \
		-t $(IMAGE_TAG) --build-arg ARCH=$(ARCH) .

.PHONY: sanity-test
sanity-test:
	make
	./test/sanity/run-test.sh
.PHONY: mod-check
mod-check:
	go mod verify && [ "$(shell sha512sum go.mod)" = "`sha512sum go.mod`" ] || ( echo "ERROR: go.mod was modified by 'go mod verify'" && false )

.PHONY: clean
clean:
	go clean -mod=vendor -r -x
	rm -f bin/iscsiplugin
