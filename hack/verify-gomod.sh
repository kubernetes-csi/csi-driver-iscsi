#!/bin/bash

# Copyright 2020 The Kubernetes Authors.
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

set -euo pipefail

echo "Verifying gomod"

# Create a temporary directory and copy everything there so this repo's working
# directory is not polluted when running the script locally in a development workflow.
# tar is used because is ubiquitous and offers the `exclude` flag.
root=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "${tmp}"' EXIT
tar -C "${root}" \
	--exclude=./.git \
	--exclude=./vendor \
	--exclude=./bin -cf \
	- . | tar -C "${tmp}" -xf -
cd "${tmp}"

export GO111MODULE=on
export GOFLAGS=

# Print the go version, mostly for logging and double verification.
go version

# From the temporary directory, run `tidy` and `vendor'
echo "go mod tidy"
go mod tidy
echo "go mod vendor"
go mod vendor

# If vendor changes were triggered in the temporary directory, warn the user.
status=0
diff -u "${root}/go.mod" go.mod || status=1
diff -u "${root}/go.sum" go.sum || status=1
diff -qr "${root}/vendor" vendor || status=1
if [[ ${status} -ne 0 ]]; then
	echo "error: module files or vendor are out of date"
	exit "${status}"
fi
echo "Done"
