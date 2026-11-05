#!/usr/bin/env bash
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

set -euo pipefail

cluster=${E2E_KIND_CLUSTER:-kind}
node="${cluster}-control-plane"
kind create cluster --name "$cluster" --wait 120s
docker exec "$node" bash -c '
    set -e
    if ! dpkg -s open-iscsi udev >/dev/null 2>&1; then
        apt-get update
        DEBIAN_FRONTEND=noninteractive apt-get install -y --no-upgrade open-iscsi udev
    fi
    if [ -n "$(ls -A /sys/class/iscsi_session)" ]; then
        echo "Active iSCSI sessions exist on the host; refusing to start." >&2
        exit 1
    fi
    mount -o remount,rw /sys
    printf "%s\n" \
      "ACTION==\"add\", SUBSYSTEM==\"block\", ENV{ID_PATH}==\"*iscsi-iqn.2026-01.com.test:storage-*\", RUN+=\"/usr/bin/mknod /dev/%k b %M %m\"" \
      "ACTION==\"remove\", SUBSYSTEM==\"block\", ENV{ID_PATH}==\"*iscsi-iqn.2026-01.com.test:storage-*\", RUN+=\"/usr/bin/rm -f /dev/%k\"" \
      > /etc/udev/rules.d/99-iscsi-e2e.rules
    systemctl stop iscsid.service iscsid.socket
    systemctl start systemd-udevd
    udevadm control --reload
    mkdir -p /run/lock
'
node_pid=$(docker inspect --format '{{.State.Pid}}' "$node")
docker run -d --name "${cluster}-iscsid" --privileged --pid=host \
	--network "container:$node" -e "NODE_PID=$node_pid" --entrypoint python3 \
	python:3.13.12-alpine3.23@sha256:bb1f2fdb1065c85468775c9d680dcd344f6442a2d1181ef7916b60a623f11d40 \
	-c "$(cat test/utils/kind-iscsid.py)"
for ((i = 0; i < 30; i++)); do
	if docker exec "$node" pgrep -x iscsid >/dev/null; then
		exit 0
	fi
	if [ "$(docker inspect --format '{{.State.Running}}' "${cluster}-iscsid")" != true ]; then
		docker logs "${cluster}-iscsid" >&2
		exit 1
	fi
	sleep 1
done
echo "iscsid did not start" >&2
exit 1
