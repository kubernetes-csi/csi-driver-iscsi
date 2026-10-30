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

FROM golang:1.26.10-bookworm AS builder
ARG TARGETARCH
ARG ARCH=${TARGETARCH}
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY test/e2e/targetfixture ./test/e2e/targetfixture
COPY test/e2e/cmd/tgtd-fixture ./test/e2e/cmd/tgtd-fixture
RUN CGO_ENABLED=0 GOARCH=${ARCH} go build -mod=vendor -trimpath -o /tgtd-fixture ./test/e2e/cmd/tgtd-fixture

FROM ubuntu:22.04@sha256:e0f80c829509ef4df9e76bd8944eb7b8ec25cbb379b112a7f9448b2c77a77c9b

RUN apt-get update && \
    apt-get install -y --no-install-recommends tgt && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /tgtd-fixture /tgtd-fixture

EXPOSE 3260

ENTRYPOINT ["/tgtd-fixture", "serve"]
