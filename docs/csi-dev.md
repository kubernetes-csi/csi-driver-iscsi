# ISCSI CSI driver development guide

## How to build this project

- Clone repo

```console
$ mkdir -p $GOPATH/src/sigs.k8s.io/
$ git clone https://github.com/kubernetes-csi/csi-driver-iscsi $GOPATH/src/github.com/kubernetes-csi/csi-driver-iscsi
```

- Build CSI driver

```console
$ cd $GOPATH/src/github.com/kubernetes-csi/csi-driver-iscsi
$ make
```

## How to test CSI driver in local environment

Install `csc` tool according to https://github.com/rexray/gocsi/tree/master/csc

```console
$ mkdir -p $GOPATH/src/github.com
$ cd $GOPATH/src/github.com
$ git clone https://github.com/rexray/gocsi.git
$ cd rexray/gocsi/csc
$ make build
```

#### Start CSI driver locally

```console
$ cd $GOPATH/src/github.com/kubernetes-csi/csi-driver-iscsi
$ ./_output/iscsiplugin --endpoint tcp://127.0.0.1:10000 --nodeid CSINode -v=5 &
```

- Get plugin info

```console
$ csc identity plugin-info --endpoint "$endpoint"
"iscsi.csi.k8s.io"    "v2.0.0"
```

- Publish an iscsi volume

```console
$ export ISCSI_TARGET="iSCSI Target Server IP (Ex: 10.10.10.10)"
$ export IQN="Target IQN"
$ csc node publish --endpoint tcp://127.0.0.1:10000 --target-path /mnt/iscsi --attrib targetPortal=$ISCSI_TARGET --attrib iqn=$IQN --attrib lun=<lun-id> iscsitestvol
iscsitestvol
```

- Unpublish an iscsi volume

```console
$ csc node unpublish --endpoint tcp://127.0.0.1:10000 --target-path /mnt/iscsi iscsitestvol
iscsitestvol
```

- Validate volume capabilities

```console
$ csc controller validate-volume-capabilities --endpoint "$endpoint" --cap "$cap" "$volumeid"
```

- Get NodeID

```console
$ csc node get-info --endpoint "$endpoint"
CSINode
```

## How to test CSI driver in a Kubernetes cluster

- Set environment variable

```console
export REGISTRY=<dockerhub-alias>
export IMAGE_VERSION=latest
```

- Build container image and push image to dockerhub

```console
# build docker image
make container
```
