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

package testsuites

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"

	testdriver "github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/driver"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
)

func Preflight(ctx context.Context, client kubernetes.Interface, cfg Config) error {
	cfg = cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.CommandTimeout)
	defer cancel()
	selector := metav1.ListOptions{LabelSelector: OwnerLabel + "=" + cfg.RunID}
	refuse := func(count int, err error) error {
		if err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("refusing to reuse existing resources")
		}
		return nil
	}
	namespaces, err := client.CoreV1().Namespaces().List(ctx, selector)
	if err != nil {
		return err
	}
	if err = refuse(len(namespaces.Items), nil); err != nil {
		return err
	}
	volumes, err := client.CoreV1().PersistentVolumes().List(ctx, selector)
	if err != nil {
		return err
	}
	if err = refuse(len(volumes.Items), nil); err != nil {
		return err
	}
	drivers, err := client.StorageV1().CSIDrivers().List(ctx, selector)
	if err != nil {
		return err
	}
	if err = refuse(len(drivers.Items), nil); err != nil {
		return err
	}
	sets, err := client.AppsV1().DaemonSets(DriverNamespace).List(ctx, selector)
	if err != nil {
		return err
	}
	if err = refuse(len(sets.Items), nil); err != nil {
		return err
	}
	maps, err := client.CoreV1().ConfigMaps(DriverNamespace).List(ctx, selector)
	if err != nil {
		return err
	}
	if err = refuse(len(maps.Items), nil); err != nil {
		return err
	}
	sets, err = client.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{LabelSelector: "app=" + DriverDaemonSet})
	if err != nil {
		return err
	}
	if err = refuse(len(sets.Items), nil); err != nil {
		return err
	}
	_, err = client.CoreV1().Namespaces().Get(ctx, cfg.Namespace, metav1.GetOptions{})
	if err == nil {
		return fmt.Errorf("refusing existing namespace")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = client.StorageV1().CSIDrivers().Get(ctx, DriverName, metav1.GetOptions{})
	if err == nil {
		return fmt.Errorf("refusing existing CSI driver")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = client.AppsV1().DaemonSets(DriverNamespace).Get(ctx, DriverDaemonSet, metav1.GetOptions{})
	if err == nil {
		return fmt.Errorf("refusing existing driver DaemonSet")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = client.CoreV1().ConfigMaps(DriverNamespace).Get(ctx, DriverConfigMap, metav1.GetOptions{})
	if err == nil {
		return fmt.Errorf("refusing existing driver ConfigMap")
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	for _, prefix := range []string{"iscsi-pv", "iscsi-pv-b"} {
		_, err = client.CoreV1().PersistentVolumes().Get(ctx, prefix+"-"+cfg.RunID, metav1.GetOptions{})
		if err == nil {
			return fmt.Errorf("refusing existing persistent volume")
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (h *Harness) Prepare(ctx context.Context) error {
	if err := Preflight(ctx, h.Client, h.Config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, h.Config.CommandTimeout)
	defer cancel()
	_, err := h.Client.CoreV1().Namespaces().Create(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.Config.Namespace, Labels: h.labels()}}, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	h.createdNamespace = true
	if err = h.createValidators(ctx); err != nil {
		return fmt.Errorf("node prerequisites: %w", err)
	}
	if err = h.createTarget(ctx); err != nil {
		return fmt.Errorf("target setup: %w", err)
	}
	if err = h.connect(ctx); err != nil {
		return fmt.Errorf("target connectivity: %w", err)
	}
	if err = h.createDriver(ctx); err != nil {
		return fmt.Errorf("driver install: %w", err)
	}
	return nil
}

func (h *Harness) createValidators(ctx context.Context) error {
	privileged := true
	name := h.name("iscsi-init")
	labels := h.labels()
	labels["app"] = name
	set := &apps.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.Config.Namespace, Labels: h.labels()}, Spec: apps.DaemonSetSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}, Template: v1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: v1.PodSpec{
			HostPID: true, HostNetwork: true, NodeSelector: map[string]string{"kubernetes.io/os": "linux"},
			Tolerations: []v1.Toleration{{Operator: v1.TolerationOpExists}},
			Containers: []v1.Container{{Name: "prerequisites", Image: h.Config.ToolsImage, ImagePullPolicy: h.Config.ToolsPullPolicy, Command: []string{"/e2e-tools", "hold"}, SecurityContext: &v1.SecurityContext{Privileged: &privileged},
				ReadinessProbe: &v1.Probe{ProbeHandler: v1.ProbeHandler{Exec: &v1.ExecAction{Command: []string{"/e2e-tools", "prerequisites", "--root=/host"}}}, InitialDelaySeconds: 1, PeriodSeconds: 2, TimeoutSeconds: 5},
				VolumeMounts:   []v1.VolumeMount{{Name: "host-root", MountPath: "/host", ReadOnly: true}},
			}}, Volumes: []v1.Volume{{Name: "host-root", VolumeSource: v1.VolumeSource{HostPath: &v1.HostPathVolumeSource{Path: "/"}}}},
		}},
	}}
	if _, err := h.Client.AppsV1().DaemonSets(h.Config.Namespace).Create(ctx, set, metav1.CreateOptions{}); err != nil {
		return err
	}
	if err := h.waitDaemonSet(ctx, h.Config.Namespace, name); err != nil {
		return err
	}
	pods, err := h.validators(ctx)
	if err != nil {
		return err
	}
	if h.VerifyTools == nil {
		return fmt.Errorf("tools image identity verifier is required")
	}
	return h.VerifyTools(ctx, pods)
}

func (h *Harness) waitDaemonSet(ctx context.Context, namespace, name string) error {
	return h.poll(ctx, func(ctx context.Context) (bool, error) {
		set, err := h.Client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return set.Status.ObservedGeneration >= set.Generation && set.Status.DesiredNumberScheduled > 0 && set.Status.NumberReady == set.Status.DesiredNumberScheduled && set.Status.UpdatedNumberScheduled == set.Status.DesiredNumberScheduled, nil
	})
}

func (h *Harness) createTarget(ctx context.Context) error {
	name := h.name("iscsi-target")
	one := int32(1)
	privileged := true
	podLabels := h.labels()
	podLabels["app"] = name
	set := &apps.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.Config.Namespace, Labels: h.labels()}, Spec: apps.DeploymentSpec{Replicas: &one,
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}, Template: v1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: podLabels}, Spec: v1.PodSpec{
			NodeSelector: map[string]string{"kubernetes.io/os": "linux"}, Containers: []v1.Container{{Name: "tgtd", Image: h.Config.TargetImage, ImagePullPolicy: h.Config.TargetPullPolicy, SecurityContext: &v1.SecurityContext{Privileged: &privileged},
				ReadinessProbe: &v1.Probe{ProbeHandler: v1.ProbeHandler{Exec: &v1.ExecAction{Command: []string{"/tgtd-fixture", "ready"}}}, InitialDelaySeconds: 1, PeriodSeconds: 2, TimeoutSeconds: 5},
				Env:            []v1.EnvVar{{Name: "DISK_SIZE", Value: "1024"}, {Name: "LUN_COUNT", Value: strconv.Itoa(h.Config.LUNCount)}, {Name: "IQN", Value: h.Config.IQN}},
				Ports:          []v1.ContainerPort{{ContainerPort: 3260}}, VolumeMounts: []v1.VolumeMount{{Name: "target-data", MountPath: "/var/lib/iscsi"}},
			}}, Volumes: []v1.Volume{{Name: "target-data", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}}},
		}},
	}}
	if _, err := h.Client.CoreV1().Services(h.Config.Namespace).Create(ctx, &v1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.Config.Namespace, Labels: h.labels()}, Spec: v1.ServiceSpec{Selector: map[string]string{"app": name}, Ports: []v1.ServicePort{{Port: 3260, TargetPort: intstr.FromInt32(3260)}}}}, metav1.CreateOptions{}); err != nil {
		return err
	}
	if _, err := h.Client.AppsV1().Deployments(h.Config.Namespace).Create(ctx, set, metav1.CreateOptions{}); err != nil {
		return err
	}
	if err := h.poll(ctx, func(ctx context.Context) (bool, error) {
		d, e := h.Client.AppsV1().Deployments(h.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			return false, e
		}
		return d.Status.ObservedGeneration >= d.Generation && d.Status.ReadyReplicas == 1 && d.Status.UpdatedReplicas == 1, nil
	}); err != nil {
		return err
	}
	pod, err := h.targetPod(ctx)
	if err != nil {
		return err
	}
	h.portal = net.JoinHostPort(pod.Status.PodIP, "3260")
	return nil
}

func readyPod(pod *v1.Pod) bool {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != v1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

func (h *Harness) targetPod(ctx context.Context) (*v1.Pod, error) {
	pods, err := h.Client.CoreV1().Pods(h.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + h.name("iscsi-target")})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) != 1 {
		return nil, fmt.Errorf("target requires exactly one pod")
	}
	pod := &pods.Items[0]
	if pod.UID == "" || !h.owned(pod.Labels) || !readyPod(pod) || net.ParseIP(pod.Status.PodIP) == nil {
		return nil, fmt.Errorf("target pod is not ready with a valid address")
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != "tgtd" || pod.Spec.Containers[0].Image != h.Config.TargetImage {
		return nil, fmt.Errorf("target does not run selected image")
	}
	if h.VerifyTarget == nil {
		return nil, fmt.Errorf("target image identity verifier is required")
	}
	if err = h.VerifyTarget(ctx, pod); err != nil {
		return nil, err
	}
	return pod, nil
}

func (h *Harness) connect(ctx context.Context) error {
	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: h.name("iscsi-check"), Namespace: h.Config.Namespace, Labels: h.labels()}, Spec: v1.PodSpec{RestartPolicy: v1.RestartPolicyNever, Containers: []v1.Container{{Name: "test", Image: h.Config.ToolsImage, ImagePullPolicy: h.Config.ToolsPullPolicy, Command: []string{"/e2e-tools", "connect", "--address=" + h.portal, "--timeout=" + h.Config.RequestTimeout.String()}}}}}
	if _, err := h.Client.CoreV1().Pods(h.Config.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return err
	}
	return h.poll(ctx, func(ctx context.Context) (bool, error) {
		p, e := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if e != nil {
			return false, e
		}
		if p.Status.Phase == v1.PodFailed {
			return false, fmt.Errorf("target connectivity failed")
		}
		return p.Status.Phase == v1.PodSucceeded, nil
	})
}

func (h *Harness) createDriver(ctx context.Context) error {
	for _, name := range []string{"csi-iscsi-driverinfo.yaml", "csi-iscsi-node.yaml"} {
		data, err := os.ReadFile(filepath.Join(h.Config.RepoRoot, "deploy", name))
		if err != nil {
			return err
		}
		reader := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
		decoder := serializer.NewCodecFactory(scheme.Scheme).UniversalDeserializer()
		for {
			document, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if len(bytes.TrimSpace(document)) == 0 {
				continue
			}
			jsonData, err := yaml.ToJSON(document)
			if err != nil {
				return err
			}
			object, _, err := decoder.Decode(jsonData, nil, nil)
			if err != nil {
				return err
			}
			switch value := object.(type) {
			case *storage.CSIDriver:
				value.Labels = h.labels()
				_, err = h.Client.StorageV1().CSIDrivers().Create(ctx, value, metav1.CreateOptions{})
			case *v1.ConfigMap:
				value.Labels = h.labels()
				_, err = h.Client.CoreV1().ConfigMaps(DriverNamespace).Create(ctx, value, metav1.CreateOptions{})
			case *apps.DaemonSet:
				value.Labels = h.labels()
				if value.Spec.Template.Labels == nil {
					value.Spec.Template.Labels = map[string]string{}
				}
				for key, label := range h.labels() {
					value.Spec.Template.Labels[key] = label
				}
				found := 0
				for i := range value.Spec.Template.Spec.Containers {
					c := &value.Spec.Template.Spec.Containers[i]
					if c.Name == "iscsi" {
						c.Image = h.Config.DriverImage
						c.ImagePullPolicy = h.Config.DriverPullPolicy
						found++
					}
				}
				if found != 1 {
					return fmt.Errorf("local driver manifest must contain one iscsi container")
				}
				_, err = h.Client.AppsV1().DaemonSets(DriverNamespace).Create(ctx, value, metav1.CreateOptions{})
			default:
				return fmt.Errorf("unsupported driver manifest object %T", value)
			}
			if err != nil {
				return err
			}
		}
	}
	if err := h.waitDaemonSet(ctx, DriverNamespace, DriverDaemonSet); err != nil {
		return err
	}
	return h.verifyDriver(ctx)
}

func (h *Harness) verifyDriver(ctx context.Context) error {
	pods, err := h.Client.CoreV1().Pods(DriverNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + DriverDaemonSet})
	if err != nil {
		return err
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no driver pods found")
	}
	for i := range pods.Items {
		if !h.owned(pods.Items[i].Labels) || !readyPod(&pods.Items[i]) {
			return fmt.Errorf("driver pods not owned and ready")
		}
	}
	if h.VerifyDriver == nil {
		return fmt.Errorf("driver image identity verifier is required")
	}
	return h.VerifyDriver(ctx, pods.Items)
}

func (h *Harness) createVolume(ctx context.Context, suffix string, lun int, node string, worker bool) error {
	volume, claim, pod := h.name("iscsi-pv"+suffix), h.name("iscsi-pvc"+suffix), h.name("iscsi-pod"+suffix)
	if _, err := h.Client.CoreV1().PersistentVolumes().Create(ctx, testdriver.PersistentVolume(volume, h.Config.Namespace, claim, h.portal, h.Config.IQN, lun, h.labels()), metav1.CreateOptions{}); err != nil {
		return err
	}
	if _, err := h.Client.CoreV1().PersistentVolumeClaims(h.Config.Namespace).Create(ctx, testdriver.PersistentVolumeClaim(claim, h.Config.Namespace, volume, h.labels()), metav1.CreateOptions{}); err != nil {
		return err
	}
	return h.createWorkload(ctx, pod, claim, node, worker)
}

func (h *Harness) createWorkload(ctx context.Context, name, claim, node string, worker bool) error {
	command := []string{"/e2e-tools", "hold"}
	if worker {
		command = []string{"/e2e-tools", "io", "--dir=/mnt", "--progress=/tmp/io-progress", "--timeout=" + h.Config.SharedIOTimeout.String()}
	}
	pod := testdriver.Pod(name, h.Config.Namespace, claim, h.Config.ToolsImage, node, h.Config.ToolsPullPolicy, h.labels(), command)
	if worker {
		pod.Spec.Containers[0].ReadinessProbe = &v1.Probe{ProbeHandler: v1.ProbeHandler{Exec: &v1.ExecAction{Command: []string{"/e2e-tools", "progress", "--path=/tmp/io-progress"}}}, PeriodSeconds: 1, TimeoutSeconds: 2}
	}
	h.published = true // arm before create: an API timeout can follow a successful admission
	if _, err := h.Client.CoreV1().Pods(h.Config.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return err
	}
	return h.waitPod(ctx, name)
}

func (h *Harness) waitPod(ctx context.Context, name string) error {
	return h.poll(ctx, func(ctx context.Context) (bool, error) {
		p, e := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			return false, e
		}
		if p.Status.Phase == v1.PodFailed {
			return false, fmt.Errorf("test pod failed")
		}
		return readyPod(p), nil
	})
}
