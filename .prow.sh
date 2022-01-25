#! /bin/bash -e

# Copyright 2021 The Kubernetes Authors.
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


# A Prow job can override these defaults, but this shouldn't be necessary.

# disable e2e tests for now
CSI_PROW_E2E_REPO=none

# Only these tests make sense for unit test
: ${CSI_PROW_TESTS:="unit"}

# What matters for csi-sanity is the version of the hostpath driver
# that we test against, not the version of Kubernetes that it runs on.
# We pick the latest stable Kubernetes here because the corresponding
# deployment has the current driver. v1.0.1 (from the current 1.13
# deployment) does not pass csi-sanity testing.
: ${CSI_PROW_KUBERNETES_VERSION:=1.14.0}

## This repo supports and wants sanity testing.
#CSI_PROW_TESTS_SANITY=sanity

. release-tools/prow.sh


main
