/*
Copyright 2026 The Kubernetes Authors.

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

package driver

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
)

func TestDriver(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Static iSCSI volume builders") }

var _ = Describe("Static iSCSI resources", func() {
	It("binds an explicit retained LUN without enabling a provisioner", func() {
		labels := map[string]string{"owner": "unique"}
		volume := PersistentVolume("volume", "ns", "claim", "[2001:db8::7]:3260", "iqn.test:target", 2, labels)
		claim := PersistentVolumeClaim("claim", "ns", "volume", labels)
		Expect(volume.Spec.PersistentVolumeReclaimPolicy).To(Equal(v1.PersistentVolumeReclaimRetain))
		Expect(volume.Spec.CSI.VolumeAttributes).To(Equal(map[string]string{"targetPortal": "[2001:db8::7]:3260", "iqn": "iqn.test:target", "lun": "2", "portals": "[]", "discovery": "true"}))
		Expect(volume.Spec.CSI.Driver).To(Equal(Name))
		Expect(volume.Spec.CSI.VolumeHandle).To(Equal("volume"))
		Expect(volume.Spec.ClaimRef.Name).To(Equal(claim.Name))
		Expect(volume.Spec.ClaimRef.Namespace).To(Equal(claim.Namespace))
		Expect(claim.Spec.VolumeName).To(Equal(volume.Name))
		Expect(claim.Spec.StorageClassName).NotTo(BeNil())
		Expect(*claim.Spec.StorageClassName).To(BeEmpty())
		Expect(volume.Labels).To(Equal(labels))
		Expect(claim.Labels).To(Equal(labels))
	})
	It("uses the selected Go worker image and command on the requested node", func() {
		pod := Pod("pod", "ns", "claim", "tools:unique", "node1", v1.PullNever, map[string]string{"owner": "unique"}, []string{"/e2e-tools", "hold"})
		Expect(pod.Spec.Containers[0].Command).To(Equal([]string{"/e2e-tools", "hold"}))
		Expect(pod.Spec.Containers[0].Image).To(Equal("tools:unique"))
		Expect(pod.Spec.Containers[0].ImagePullPolicy).To(Equal(v1.PullNever))
		Expect(pod.Spec.NodeName).To(Equal("node1"))
		Expect(pod.Spec.RestartPolicy).To(Equal(v1.RestartPolicyNever))
		Expect(pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName).To(Equal("claim"))
	})
})
