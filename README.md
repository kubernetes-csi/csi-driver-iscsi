# CSI ISCSI driver

## Usage:

### Start ISCSI driver
```
$ sudo ./_output/iscsidriver --endpoint tcp://127.0.0.1:10000 --nodeid CSINode
```

### Test using csc
Get ```csc``` tool from https://github.com/rexray/gocsi/tree/master/csc

#### Get plugin info
```
$ csc identity plugin-info --endpoint tcp://127.0.0.1:10000
"ISCSI"	"0.1.0"
```

#### NodePublish a volume
```
$ export ISCSI_TARGET="iSCSI Target Server IP (Ex: 10.10.10.10)"
$ export IQN="Target IQN"
$ csc node publish --endpoint tcp://127.0.0.1:10000 --target-path /mnt/iscsi --attrib targetPortal=$ISCSI_TARGET --attrib iqn=$IQN --attrib lun=<lun-id> iscsitestvol
iscsitestvol
```

#### NodeUnpublish a volume
```
$ csc node unpublish --endpoint tcp://127.0.0.1:10000 --target-path /mnt/iscsi iscsitestvol
iscsitestvol
```

#### Get NodeID
```
$ csc node get-id --endpoint tcp://127.0.0.1:10000
CSINode
```

## Running Kubernetes End To End tests on an ISCSI Driver

First, stand up a local cluster `ALLOW_PRIVILEGED=1 hack/local-up-cluster.sh` (from your Kubernetes repo)
For Fedora/RHEL clusters, the following might be required:
  ```
  sudo chown -R $USER:$USER /var/run/kubernetes/
  sudo chown -R $USER:$USER /var/lib/kubelet
  sudo chcon -R -t svirt_sandbox_file_t /var/lib/kubelet
  ```
If you are plannig to test using your own private image, you could either install your ISCSI driver using your own set of YAML files, or edit the existing YAML files to use that private image.

When using the [existing set of YAML files](https://github.com/kubernetes-csi/csi-driver-iscsi/tree/master/deploy/kubernetes), you would edit the [csi-attacher-iscsiplugin.yaml](https://github.com/kubernetes-csi/csi-driver-iscsi/blob/master/deploy/kubernetes/csi-attacher-iscsiplugin.yaml#L46) and [csi-nodeplugin-iscsiplugin.yaml](https://github.com/kubernetes-csi/csi-driver-iscsi/blob/master/deploy/kubernetes/csi-nodeplugin-iscsiplugin.yaml#L45) files to include your private image instead of the default one. After editing these files, skip to step 3 of the following steps.

If you already have a driver installed, skip to step 4 of the following steps.

1) Build the ISCSI driver by running `make`
2) Create ISCSI Driver Image, where the image tag would be whatever that is required by your YAML deployment files        `docker build -t quay.io/k8scsi/iscsiplugin:v1.0.0 .`
3) Install the Driver: `kubectl create -f deploy/kubernetes`
4) Build E2E test binary: `make build-tests`
5) Run E2E Tests using the following command: `./bin/tests --ginkgo.v --ginkgo.progress --kubeconfig=/var/run/kubernetes/admin.kubeconfig`


## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack channel](https://kubernetes.slack.com/messages/sig-storage)
- [Mailing list](https://groups.google.com/forum/#!forum/kubernetes-sig-storage)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

[owners]: https://git.k8s.io/community/contributors/guide/owners.md
[Creative Commons 4.0]: https://git.k8s.io/website/LICENSE
