# csi lib-iscsi

A simple go package intended to assist CSI plugin authors by providing a tool set to manage iscsi connections.

## Goals

Provide a basic, lightweight library for CSI Plugin Authors to leverage some of the common tasks like connecting
and disconnecting iscsi devices to a node.  This library includes a high level abstraction for iscsi that exposes
simple Connect and Disconnect functions.  These are built on top of exported iscsiadm calls, so if you need more
control you can access the iscsiadm calls directly.

## Design Philosophy

The idea is to keep this as lightweight and generic as possible.  We intentionally avoid the use of any third party
libraries or packages in this project.  We don't have a vendor directory, because we attempt to rely only on the std
golang libs.  This may prove to not be ideal, and may be changed over time, but initially it's a worthwhile goal.

## Logging and Debug

By default the library does not provide any logging, but provides an error message that includes any messages from
iscsiadm as well as exit-codes.  In the event that you need to debug the library, we provide a function:

```
func EnableDebugLogging(writer io.Writer)
```

This will turn on verbose logging directed to the provided io.Writer and include the response of every iscsiadm command
issued.

## Intended Usage

Curently the intended usage of this library is simply to provide a golang package to standardize how plugins are implementing
iscsi connect and disconnect.  It's not intended to be  a "service", although that's a possible next step.  It's currenty been
used for plugins where iscsid is installed in containers only, as well as designs where it uses the nodes iscsid.  Each of these
approaches has their own pros and cons.  Currently, it's up to the plugin author to determine which model suits them best
and to deploy their node plugin appropriately.

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack](http://slack.k8s.io/)
  * sig-storage
- [Mailing List](https://groups.google.com/forum/#!forum/kubernetes-dev)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

[owners]: https://git.k8s.io/community/contributors/guide/owners.md
[Creative Commons 4.0]: https://git.k8s.io/website/LICENSE
