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

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack channel](https://kubernetes.slack.com/messages/sig-storage)
- [Mailing list](https://groups.google.com/forum/#!forum/kubernetes-sig-storage)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

[owners]: https://git.k8s.io/community/contributors/guide/owners.md
[Creative Commons 4.0]: https://git.k8s.io/website/LICENSE
