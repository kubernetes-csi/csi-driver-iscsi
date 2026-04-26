module github.com/kubernetes-csi/csi-driver-iscsi

go 1.22.0

require (
	github.com/container-storage-interface/spec v1.10.0
	github.com/kubernetes-csi/csi-lib-iscsi v0.0.0-20230620124731-f4739f571747
	github.com/kubernetes-csi/csi-lib-utils v0.18.1
	github.com/onsi/ginkgo/v2 v2.20.2
	github.com/onsi/gomega v1.34.2
	github.com/spf13/cobra v1.8.1
	golang.org/x/net v0.30.0
	google.golang.org/grpc v1.67.1
	k8s.io/api v0.31.2
	k8s.io/apimachinery v0.31.2
	k8s.io/client-go v0.31.2
	k8s.io/klog/v2 v2.130.1
	k8s.io/mount-utils v0.31.2
	k8s.io/utils v0.0.0-20241104100929-3ea5e8cea738
)
