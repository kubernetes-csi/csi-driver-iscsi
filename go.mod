module github.com/kubernetes-csi/csi-driver-iscsi

go 1.12

require (
	github.com/beorn7/perks v0.0.0-20180321164747-3a771d992973
	github.com/container-storage-interface/spec v1.1.0
	github.com/davecgh/go-spew v1.1.1
	github.com/gogo/protobuf v1.1.1
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/golang/groupcache v0.0.0-20181024230925-c65c006176ff
	github.com/golang/protobuf v1.2.0
	github.com/google/gofuzz v0.0.0-20170612174753-24818f796faf
	github.com/googleapis/gnostic v0.2.0
	github.com/hashicorp/golang-lru v0.5.0
	github.com/inconshreveable/mousetrap v1.0.0
	github.com/json-iterator/go v1.1.5
	github.com/kubernetes-csi/csi-lib-iscsi v0.0.0-20190415173011-c545557492f4
	github.com/kubernetes-csi/csi-lib-utils v0.2.0
	github.com/matttproud/golang_protobuf_extensions v1.0.1
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd
	github.com/modern-go/reflect2 v0.0.0-20180701023420-4b7aa43c6742
	github.com/prometheus/client_golang v0.9.1
	github.com/prometheus/client_model v0.0.0-20180712105110-5c3871d89910
	github.com/prometheus/common v0.0.0-20181113130724-41aa239b4cce
	github.com/prometheus/procfs v0.0.0-20181005140218-185b4288413d
	github.com/spf13/cobra v0.0.3
	github.com/spf13/pflag v1.0.3
	golang.org/x/crypto v0.0.0-20181112202954-3d3f9f413869
	golang.org/x/net v0.0.0-20181113165502-88d92db4c548
	golang.org/x/oauth2 v0.0.0-20181106182150-f42d05182288
	golang.org/x/sys v0.0.0-20181107165924-66b7b1311ac8
	golang.org/x/text v0.3.0
	golang.org/x/time v0.0.0-20181108054448-85acf8d2951c
	google.golang.org/appengine v1.3.0
	google.golang.org/genproto v0.0.0-20181109154231-b5d43981345b
	google.golang.org/grpc v1.16.0
	gopkg.in/inf.v0 v0.9.1
	gopkg.in/yaml.v2 v2.2.1
	k8s.io/api v0.0.0-20190313235455-40a48860b5ab
	k8s.io/apiextensions-apiserver v0.0.0-20190315093550-53c4693659ed
	k8s.io/apimachinery v0.0.0-20181110190943-2a7c93004028
	k8s.io/apiserver v0.0.0-20190313205120-8b27c41bdbb1
	k8s.io/client-go v11.0.0+incompatible
	k8s.io/cloud-provider v0.0.0-20190314002645-c892ea32361a
	k8s.io/klog v0.3.0
	k8s.io/kube-openapi v0.0.0-20181109181836-c59034cc13d5
	k8s.io/kubernetes v1.14.3
	k8s.io/utils v0.0.0-20190607212802-c55fbcfc754a
	sigs.k8s.io/yaml v1.1.0
)
