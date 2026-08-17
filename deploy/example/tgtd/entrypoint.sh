#!/bin/bash

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

set -e

DISK_SIZE=${DISK_SIZE:-1024}  # Size in MB, default 1GB
IQN=${IQN:-"iqn.2026-01.com.test:storage"}
DISK_PATH="/var/lib/iscsi/disk0.img"

mkdir -p /var/lib/iscsi

echo "Creating ${DISK_SIZE}MB disk image at ${DISK_PATH}..."
dd if=/dev/zero of="$DISK_PATH" bs=1M count="$DISK_SIZE"

echo "Starting tgtd..."
tgtd -f &
TGTD_PID=$!

# Wait for tgtd to be ready
for i in $(seq 1 10); do
    if tgtadm --lld iscsi --op show --mode sys > /dev/null 2>&1; then
        break
    fi
    echo "Waiting for tgtd to start... ($i/10)"
    sleep 1
done

echo "Configuring iSCSI target..."
tgtadm --lld iscsi --op new --mode target --tid 1 -T "$IQN"
tgtadm --lld iscsi --op new --mode logicalunit --tid 1 --lun 1 -b "$DISK_PATH"
tgtadm --lld iscsi --op bind --mode target --tid 1 -I ALL

echo "========================================="
echo "iSCSI target ready:"
echo "  IQN:  $IQN"
echo "  LUN:  1"
echo "  Disk: $DISK_PATH (${DISK_SIZE}MB)"
echo "========================================="
tgtadm --lld iscsi --op show --mode target

wait $TGTD_PID
