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

# Step 0: Ensure open-iscsi is installed on all nodes
echo "[0/7] Ensuring open-iscsi is installed on cluster nodes..."
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: iscsi-init
  namespace: kube-system
spec:
  selector:
    matchLabels:
      app: iscsi-init
  template:
    metadata:
      labels:
        app: iscsi-init
    spec:
      hostPID: true
      hostNetwork: true
      nodeSelector:
        kubernetes.io/os: linux
      containers:
        - name: iscsi-init
          image: ubuntu:22.04
          securityContext:
            privileged: true
          command:
            - nsenter
            - --target
            - "1"
            - --mount
            - --uts
            - --ipc
            - --net
            - --pid
            - --
            - bash
            - -c
            - |
              if ! command -v iscsiadm &>/dev/null; then
                echo "Installing open-iscsi..."
                apt-get update -qq && apt-get install -y -qq open-iscsi >/dev/null 2>&1 || \
                yum install -y -q iscsi-initiator-utils >/dev/null 2>&1 || \
                echo "WARNING: Could not install open-iscsi"
              fi
              # Ensure iscsid is running
              if command -v systemctl &>/dev/null; then
                systemctl enable iscsid --now 2>/dev/null || true
              else
                iscsid 2>/dev/null || true
              fi
              echo "open-iscsi ready"
              sleep infinity
          volumeMounts:
            - name: host-root
              mountPath: /host
      volumes:
        - name: host-root
          hostPath:
            path: /
EOF
echo "Waiting for iscsi-init DaemonSet to be ready..."
kubectl rollout status daemonset/iscsi-init -n kube-system --timeout=300s
# Give iscsid a moment to start
sleep 5
echo "open-iscsi installed on all nodes."

# Step 1: Deploy iSCSI target server
echo "[1/7] Deploying iSCSI target server..."
kubectl apply -f "${REPO_ROOT}/deploy/example/iscsi-server.yaml"
kubectl rollout status deployment/iscsi-server -n "${NAMESPACE}" --timeout=120s
echo "iSCSI target server is ready."

# Step 2: Verify iSCSI target is accessible
echo "[2/7] Verifying iSCSI target is accessible..."
kubectl run iscsi-check --rm -i --restart=Never --image=busybox:1.36 -- \
  sh -c "echo | nc -w 3 iscsi-server.${NAMESPACE}.svc.cluster.local 3260 && echo 'port open' || echo 'port closed'"
echo "iSCSI target connectivity check complete."

# Step 3: Install CSI driver
echo "[3/7] Installing iSCSI CSI driver..."
"${REPO_ROOT}/deploy/install-driver.sh"
echo "Waiting for CSI driver DaemonSet to be ready..."
kubectl rollout status daemonset/csi-iscsi-node -n kube-system --timeout=120s
echo "iSCSI CSI driver installed."

# Step 4: Create PV
echo "[4/7] Creating PersistentVolume..."
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

# Step 5: Create PVC
echo "[5/7] Creating PersistentVolumeClaim..."
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

# Step 6: Create test pod
echo "[6/7] Creating test pod..."
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
kubectl wait --for=condition=ready pod/iscsi-e2e-test-pod -n "${NAMESPACE}" --timeout=300s
echo "Test pod is ready."

# Step 7: Verify
echo "[7/7] Verifying mount..."
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
kubectl delete daemonset iscsi-init -n kube-system 2>/dev/null || true
"${REPO_ROOT}/deploy/uninstall-driver.sh" 2>/dev/null || true

exit $EXIT_CODE
