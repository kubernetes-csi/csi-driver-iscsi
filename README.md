# CSI ISCSI driver

The CSI ISCSI driver is a sidecar container that dynamically attach/mount,
detach/unmount of volumes by performing Node operations as a response to the
kubelet requests when workload/application pod get scheduled to a node based on
CSI GRPC calls.

## Compatibility

This information reflects the head of this branch.

| Compatible with CSI Version | Container Image | [Min K8s Version](https://kubernetes-csi.github.io/docs/kubernetes-compatibility.html#minimum-version) | [Recommended K8s Version](https://kubernetes-csi.github.io/docs/kubernetes-compatibility.html#recommended-version) |
| ------------------------------------------------------------------------------------------ | --------------------------------------------------| --------------- | ------------- |
| [CSI Spec v1.5.0](https://github.com/container-storage-interface/spec/releases/tag/v1.5.0) | gcr.io/k8s-staging-sig-storage/iscsiplugin:canary | 1.20            | 1.22          |

## Status

The
release [v0.1.0](https://github.com/kubernetes-csi/csi-driver-iscsi/releases/tag/v0.1.0)
is available as a pre-release for trying out this driver.

## Usage:

### Start ISCSI driver

```
$ sudo ./bin/iscsiplugin --endpoint tcp://127.0.0.1:10000 --nodeid CSINode
```

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

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on
the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack channel](https://kubernetes.slack.com/messages/sig-storage)
- [Mailing list](https://groups.google.com/forum/#!forum/kubernetes-sig-storage)

### Code of conduct

Participation in the Kubernetes community is governed by
the [Kubernetes Code of Conduct](code-of-conduct.md).

[owners]: https://git.k8s.io/community/contributors/guide/owners.md

[Creative Commons 4.0]: https://git.k8s.io/website/LICENSE
