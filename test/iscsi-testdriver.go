/*
Copyright 2019 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package test

import (
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/volume"
	"k8s.io/kubernetes/test/e2e/storage/testpatterns"
	"k8s.io/kubernetes/test/e2e/storage/testsuites"
)

type iSCSIDriver struct {
	driverInfo testsuites.DriverInfo
	manifests  []string
}

var ISCSIdriver = InitISCSIDriver

type iSCSIVolume struct {
	serverIP  string
	serverPod *v1.Pod
	f         *framework.Framework
	iqn       string
}

// initISCSIDriver returns ISCSIDriver that implements TestDriver interface
func initISCSIDriver(name string, manifests ...string) testsuites.TestDriver {
	return &iSCSIDriver{
		driverInfo: testsuites.DriverInfo{
			Name:        name,
			MaxFileSize: testpatterns.FileSizeMedium,
			SupportedFsType: sets.NewString(
				"", // Default fsType
				"ext2",
				// TODO: fix iSCSI driver can work with ext3
				//"ext3",
				"ext4",
			),
			Capabilities: map[testsuites.Capability]bool{
				testsuites.CapPersistence: true,
				testsuites.CapFsGroup:     true,
				testsuites.CapExec:        true,
			},
		},
		manifests: manifests,
	}
}

func InitISCSIDriver() testsuites.TestDriver {

	return initISCSIDriver("csi-iscsiplugin",
		"csi-attacher-iscsiplugin.yaml",
		"csi-attacher-rbac.yaml",
		"csi-nodeplugin-iscsiplugin.yaml",
		"csi-nodeplugin-rbac.yaml")

}

var _ testsuites.TestDriver = &iSCSIDriver{}
var _ testsuites.PreprovisionedVolumeTestDriver = &iSCSIDriver{}
var _ testsuites.PreprovisionedPVTestDriver = &iSCSIDriver{}

func (i *iSCSIDriver) GetDriverInfo() *testsuites.DriverInfo {
	return &i.driverInfo
}

func (i *iSCSIDriver) SkipUnsupportedTest(pattern testpatterns.TestPattern) {
	if pattern.VolType == testpatterns.DynamicPV {
		framework.Skipf("iSCSI Driver does not support dynamic provisioning -- skipping")
	}
}

func (i *iSCSIDriver) GetPersistentVolumeSource(readOnly bool, fsType string, volume testsuites.TestVolume) (*v1.PersistentVolumeSource, *v1.VolumeNodeAffinity) {
	iv, _ := volume.(*iSCSIVolume)
	volSource := v1.PersistentVolumeSource{
		CSI: &v1.CSIPersistentVolumeSource{
			Driver:       i.driverInfo.Name,
			VolumeHandle: "iscsi-vol",
			VolumeAttributes: map[string]string{
				"targetPortal":      "127.0.0.1:3260",
				"portals":           "[]",
				"iqn":               iv.iqn,
				"lun":               "0",
				"iscsiInterface":    "default",
				"discoveryCHAPAuth": "false",
				"sessionCHAPAuth":   "false",
			},
		},
	}

	if fsType != "" {
		volSource.CSI.FSType = fsType
	}

	return &volSource, nil
}

func (i *iSCSIDriver) PrepareTest(f *framework.Framework) (*testsuites.PerTestConfig, func()) {
	config := &testsuites.PerTestConfig{
		Driver:    i,
		Prefix:    "iscsi",
		Framework: f,
	}

	return config, func() {}
}

func (i *iSCSIDriver) CreateVolume(config *testsuites.PerTestConfig, volType testpatterns.TestVolType) testsuites.TestVolume {
	f := config.Framework
	cs := f.ClientSet
	ns := f.Namespace

	iscsiConfig, serverPod, serverIP, iqn := volume.NewISCSIServer(cs, ns.Name)
	config.ServerConfig = &iscsiConfig
	config.ClientNodeName = iscsiConfig.ClientNodeName

	return &iSCSIVolume{
		serverPod: serverPod,
		serverIP:  serverIP,
		iqn:       iqn,
		f:         f,
	}
}

func (v *iSCSIVolume) DeleteVolume() {
	volume.CleanUpVolumeServer(v.f, v.serverPod)
}
