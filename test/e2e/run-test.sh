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

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

NAMESPACE="${NAMESPACE:-default}"
TARGET_PORTAL="iscsi-server.${NAMESPACE}.svc.cluster.local:3260"
IQN="iqn.2026-01.com.test:storage"
LUN=1

echo "=== iSCSI CSI Driver E2E Test ==="

# Step 1: Deploy iSCSI target server
echo "[1/6] Deploying iSCSI target server..."
kubectl apply -f "${REPO_ROOT}/deploy/example/iscsi-server.yaml"
kubectl rollout status deployment/iscsi-server -n "${NAMESPACE}" --timeout=120s
echo "iSCSI target server is ready."

# Step 2: Verify iSCSI target is accessible
echo "[2/6] Verifying iSCSI target is accessible..."
kubectl run iscsi-check --rm -i --restart=Never --image=busybox:1.36 -- \
  sh -c "echo | nc -w 3 iscsi-server.${NAMESPACE}.svc.cluster.local 3260 && echo 'port open' || echo 'port closed'"
echo "iSCSI target connectivity check complete."

# Step 3: Create PV
echo "[3/6] Creating PersistentVolume..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolume
metadata:
  name: iscsi-e2e-test-pv
spec:
  capacity:
    storage: 1Gi
  accessModes:
    - ReadWriteOnce
  persistentVolumeReclaimPolicy: Delete
  csi:
    driver: iscsi.csi.k8s.io
    volumeHandle: iscsi-e2e-test-pv
    volumeAttributes:
      targetPortal: "${TARGET_PORTAL}"
      iqn: "${IQN}"
      lun: "${LUN}"
      portals: "[]"
EOF
echo "PV created."

# Step 4: Create PVC
echo "[4/6] Creating PersistentVolumeClaim..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: iscsi-e2e-test-pvc
  namespace: ${NAMESPACE}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: ""
  volumeName: iscsi-e2e-test-pv
EOF
echo "PVC created."

# Step 5: Create test pod
echo "[5/6] Creating test pod..."
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: iscsi-e2e-test-pod
  namespace: ${NAMESPACE}
spec:
  containers:
    - name: test
      image: busybox:1.36
      command: ["sh", "-c", "echo 'iSCSI CSI test successful' > /mnt/test-file && cat /mnt/test-file && sleep 3600"]
      volumeMounts:
        - name: iscsi-vol
          mountPath: /mnt
  volumes:
    - name: iscsi-vol
      persistentVolumeClaim:
        claimName: iscsi-e2e-test-pvc
EOF

echo "Waiting for test pod to be ready..."
kubectl wait --for=condition=ready pod/iscsi-e2e-test-pod -n "${NAMESPACE}" --timeout=180s
echo "Test pod is ready."

# Step 6: Verify
echo "[6/6] Verifying mount..."
RESULT=$(kubectl exec iscsi-e2e-test-pod -n "${NAMESPACE}" -- cat /mnt/test-file 2>&1)
if [ "$RESULT" = "iSCSI CSI test successful" ]; then
  echo "✅ E2E test PASSED: iSCSI volume mounted and writable"
  EXIT_CODE=0
else
  echo "❌ E2E test FAILED: expected 'iSCSI CSI test successful', got '$RESULT'"
  EXIT_CODE=1
fi

# Cleanup
echo "Cleaning up..."
kubectl delete pod iscsi-e2e-test-pod -n "${NAMESPACE}" --grace-period=0 --force 2>/dev/null || true
kubectl delete pvc iscsi-e2e-test-pvc -n "${NAMESPACE}" 2>/dev/null || true
kubectl delete pv iscsi-e2e-test-pv 2>/dev/null || true
kubectl delete -f "${REPO_ROOT}/deploy/example/iscsi-server.yaml" 2>/dev/null || true

exit $EXIT_CODE
