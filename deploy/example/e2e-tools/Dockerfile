# Copyright 2026 The Kubernetes Authors.
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

# Context: repository root; --platform must match the test nodes.
# An explicit --build-arg ARCH override must match --platform.
FROM golang:1.26.10 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY test/e2e/probe/ test/e2e/probe/
COPY test/e2e/workload/ test/e2e/workload/
COPY test/e2e/cmd/e2e-tools/ test/e2e/cmd/e2e-tools/
ARG TARGETARCH
ARG ARCH=${TARGETARCH}
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} go build -mod=vendor -trimpath -o /e2e-tools ./test/e2e/cmd/e2e-tools

FROM scratch
COPY --from=build /e2e-tools /e2e-tools
ENTRYPOINT ["/e2e-tools"]
