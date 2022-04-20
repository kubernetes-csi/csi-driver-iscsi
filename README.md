# ISCSI CSI driver for Kubernetes

### Overview

This is a repository for iscsi [CSI](https://kubernetes-csi.github.io/docs/)
driver, csi plugin name: `iscsi.csi.k8s.io`. This driver requires existing and
already configured iscsi server, it could dynamically attach/mount,
detach/unmount based on CSI GRPC calls.

### Project status: Alpha

### Container Images & Kubernetes Compatibility:

|driver version  | supported k8s version | status |
|----------------|-----------------------|--------|
|master branch   | 1.19+                 | alpha   |

### Install driver on a Kubernetes cluster

- install by [kubectl](./docs/install-iscsi-csi-driver.md)

### Troubleshooting

- [CSI driver troubleshooting guide](./docs/csi-debug.md)

### Kubernetes Development

Please refer to [development guide](./docs/csi-dev.md)

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
