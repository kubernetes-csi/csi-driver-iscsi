# Install ISCSI CSI driver master version on a kubernetes cluster

## Install with kubectl

- remote install

```console
curl -skSL https://raw.githubusercontent.com/kubernetes-csi/csi-driver-iscsi/master/deploy/install-driver.sh | bash -s master --
```

- local install

```console
git clone https://github.com/kubernetes-csi/csi-driver-iscsi.git
cd csi-driver-iscsi
./deploy/install-driver.sh master local
```

- check pods status:

```console
kubectl -n kube-system get pod -o wide -l app=csi-iscsi-node
```

example output:

```console
NAME                                       READY   STATUS    RESTARTS   AGE     IP             NODE
csi-iscsi-node-cvgbs                        3/3     Running   0          35s     10.240.0.35    k8s-agentpool-22533604-1
csi-iscsi-node-dr4s4                        3/3     Running   0          35s     10.240.0.4     k8s-agentpool-22533604-0
```

- clean up ISCSI CSI driver

```console
curl -skSL https://raw.githubusercontent.com/kubernetes-csi/csi-driver-iscsi/master/deploy/uninstall-driver.sh | bash -s master --
```
