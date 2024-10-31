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

FROM registry.k8s.io/build-image/debian-base:bookworm-v1.0.4

RUN apt update && apt upgrade -y && apt-mark unhold libcap2
RUN clean-install util-linux e2fsprogs mount ca-certificates udev xfsprogs btrfs-progs open-iscsi

CMD service iscsid start
ARG ARCH
ARG binary=./bin/${ARCH}/iscsiplugin
COPY ${binary} /iscsiplugin

ENTRYPOINT ["/iscsiplugin"]
