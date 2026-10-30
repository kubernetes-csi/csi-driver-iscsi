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
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const Name = "iscsi.csi.k8s.io"

// PersistentVolume describes a pre-existing LUN. Dynamic provisioning is not
// supported by this driver and must not be implied by the test fixture.
func PersistentVolume(name, namespace, claim, portal, iqn string, lun int, labels map[string]string) *v1.PersistentVolume {
	return &v1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: v1.PersistentVolumeSpec{
		Capacity: v1.ResourceList{v1.ResourceStorage: resource.MustParse("1Gi")}, AccessModes: []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
		PersistentVolumeReclaimPolicy: v1.PersistentVolumeReclaimRetain,
		ClaimRef:                      &v1.ObjectReference{Namespace: namespace, Name: claim},
		PersistentVolumeSource: v1.PersistentVolumeSource{CSI: &v1.CSIPersistentVolumeSource{Driver: Name, VolumeHandle: name, FSType: "ext4", VolumeAttributes: map[string]string{
			"targetPortal": portal, "iqn": iqn, "lun": strconv.Itoa(lun), "portals": "[]", "discovery": "true",
		}}},
	}}
}

func PersistentVolumeClaim(name, namespace, volume string, labels map[string]string) *v1.PersistentVolumeClaim {
	empty := ""
	return &v1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Spec: v1.PersistentVolumeClaimSpec{
		AccessModes: []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce}, Resources: v1.VolumeResourceRequirements{Requests: v1.ResourceList{v1.ResourceStorage: resource.MustParse("1Gi")}},
		StorageClassName: &empty, VolumeName: volume,
	}}
}

func Pod(name, namespace, claim, image, node string, policy v1.PullPolicy, labels map[string]string, command []string) *v1.Pod {
	return &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Spec: v1.PodSpec{
		NodeName: node, NodeSelector: map[string]string{"kubernetes.io/os": "linux"}, RestartPolicy: v1.RestartPolicyNever, Containers: []v1.Container{{Name: "test", Image: image, ImagePullPolicy: policy,
			Command: command, VolumeMounts: []v1.VolumeMount{{Name: "iscsi-vol", MountPath: "/mnt"}},
		}}, Volumes: []v1.Volume{{Name: "iscsi-vol", VolumeSource: v1.VolumeSource{PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}},
	}}
}
