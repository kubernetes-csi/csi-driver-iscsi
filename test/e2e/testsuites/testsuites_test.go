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
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	clienttesting "k8s.io/client-go/testing"
)

func TestSuites(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Native iSCSI lifecycle helpers")
}

type probeError int

func (e probeError) Error() string   { return fmt.Sprintf("probe exit %d", e) }
func (e probeError) ExitStatus() int { return int(e) }

type executorFunc func(context.Context, string, string, string, []string) (string, string, error)

func (f executorFunc) Exec(ctx context.Context, ns, pod, container string, args []string) (string, string, error) {
	return f(ctx, ns, pod, container, args)
}

type inspectClient struct {
	kubernetes.Interface
	inspect func(context.Context, string, string) error
}

func (c inspectClient) CoreV1() typedcore.CoreV1Interface {
	return inspectCore{CoreV1Interface: c.Interface.CoreV1(), inspect: c.inspect}
}

type inspectCore struct {
	typedcore.CoreV1Interface
	inspect func(context.Context, string, string) error
}

func (c inspectCore) Pods(namespace string) typedcore.PodInterface {
	return inspectPods{PodInterface: c.CoreV1Interface.Pods(namespace), inspect: c.inspect}
}

type inspectPods struct {
	typedcore.PodInterface
	inspect func(context.Context, string, string) error
}

func (p inspectPods) Get(ctx context.Context, name string, options metav1.GetOptions) (*v1.Pod, error) {
	if err := p.inspect(ctx, "get", name); err != nil {
		return nil, err
	}
	return p.PodInterface.Get(ctx, name, options)
}
func (p inspectPods) Delete(ctx context.Context, name string, options metav1.DeleteOptions) error {
	if err := p.inspect(ctx, "delete", name); err != nil {
		return err
	}
	return p.PodInterface.Delete(ctx, name, options)
}

type fixture struct {
	h        *Harness
	client   *fake.Clientset
	calls    []string
	seed     int
	progress int
	mode     string
	sequence int
}

func newFixture() *fixture {
	root, err := filepath.Abs("../../..")
	Expect(err).NotTo(HaveOccurred())
	f := &fixture{client: fake.NewSimpleClientset()}
	cfg := Config{RunID: "test", Token: "unique-token", RepoRoot: root, DriverImage: "driver:test", TargetImage: "target:test", ToolsImage: "tools:test", LUNCount: 2, CommandTimeout: 5 * time.Second, RequestTimeout: 5 * time.Second, ScenarioTimeout: 10 * time.Second, TeardownTimeout: 5 * time.Second, SharedIOTimeout: time.Second}.Defaults()
	f.h = New(f.client, executorFunc(f.exec), cfg)
	f.h.PollInterval = time.Millisecond
	f.h.VerifyDriver = func(context.Context, []v1.Pod) error { return nil }
	f.h.VerifyTarget = func(context.Context, *v1.Pod) error { return nil }
	f.h.VerifyTools = func(context.Context, []v1.Pod) error { return nil }
	f.client.PrependReactor("create", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		object := action.(clienttesting.CreateAction).GetObject()
		if metadata, ok := object.(metav1.Object); ok {
			f.sequence++
			metadata.SetUID(types.UID(strconv.Itoa(f.sequence)))
			metadata.SetResourceVersion("1")
		}
		switch value := object.(type) {
		case *apps.DaemonSet:
			value.Generation = 1
			value.Status = apps.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 1, NumberReady: 1, UpdatedNumberScheduled: 1}
			pod := f.readyPod(value.Name+"-worker", value.Namespace, value.Spec.Template.Spec, value.Spec.Template.Labels)
			Expect(f.client.Tracker().Add(pod)).To(Succeed())
		case *apps.Deployment:
			value.Generation = 1
			value.Status = apps.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: 1, UpdatedReplicas: 1}
			pod := f.readyPod(value.Name+"-worker", value.Namespace, value.Spec.Template.Spec, value.Spec.Template.Labels)
			pod.Status.PodIP = "10.1.0.7"
			Expect(f.client.Tracker().Add(pod)).To(Succeed())
		case *v1.Pod:
			ready := f.readyPod(value.Name, value.Namespace, value.Spec, value.Labels)
			value.Spec = ready.Spec
			value.Status = ready.Status
			if strings.HasPrefix(value.Name, "iscsi-check-") {
				value.Status.Phase = v1.PodSucceeded
			}
			if strings.HasPrefix(value.Name, "iscsi-pod-b-") && f.mode == "b-restarted" {
				value.Status.ContainerStatuses[0].RestartCount = 1
			}
			if strings.HasPrefix(value.Name, "iscsi-pod-b-") && f.mode == "b-different-node" {
				value.Spec.NodeName = "node-other"
			}
		}
		return false, nil, nil
	})
	f.client.PrependReactor("delete", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		a := action.(clienttesting.DeleteAction)
		f.calls = append(f.calls, "delete:"+a.GetName())
		if strings.HasPrefix(a.GetName(), DriverDaemonSet) {
			old, err := f.client.Tracker().Get(v1.SchemeGroupVersion.WithResource("pods"), a.GetNamespace(), a.GetName())
			if err != nil {
				return true, nil, err
			}
			pod := old.(*v1.Pod)
			replacement := f.readyPod(pod.Name, pod.Namespace, pod.Spec, pod.Labels)
			Expect(f.client.Tracker().Delete(v1.SchemeGroupVersion.WithResource("pods"), a.GetNamespace(), a.GetName())).To(Succeed())
			Expect(f.client.Tracker().Add(replacement)).To(Succeed())
			return true, nil, nil
		}
		return false, nil, nil
	})
	f.client.PrependReactor("delete", "daemonsets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		a := action.(clienttesting.DeleteAction)
		object, err := f.client.Tracker().List(v1.SchemeGroupVersion.WithResource("pods"), v1.SchemeGroupVersion.WithKind("Pod"), a.GetNamespace())
		Expect(err).NotTo(HaveOccurred())
		for _, pod := range object.(*v1.PodList).Items {
			if pod.Labels["app"] != a.GetName() {
				continue
			}
			Expect(f.client.Tracker().Delete(v1.SchemeGroupVersion.WithResource("pods"), pod.Namespace, pod.Name)).To(Succeed())
		}
		return false, nil, nil
	})
	return f
}

func (f *fixture) readyPod(name, namespace string, spec v1.PodSpec, labels map[string]string) *v1.Pod {
	f.sequence++
	if spec.NodeName == "" {
		spec.NodeName = "node1"
	}
	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(strconv.Itoa(f.sequence)), ResourceVersion: "1", Labels: labels}, Spec: spec, Status: v1.PodStatus{Phase: v1.PodRunning, Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}}}
	for _, c := range spec.Containers {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, v1.ContainerStatus{Name: c.Name, Ready: true, State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}})
	}
	return pod
}

func (f *fixture) exec(ctx context.Context, ns, pod, container string, args []string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	command := args[1]
	f.calls = append(f.calls, "exec:"+command)
	switch command {
	case "snapshot":
		return "sdb sdc\n", "", nil
	case "snapshot-lun":
		if f.mode == "missing-device" {
			return "", "", nil
		}
		return "sdb\n", "", nil
	case "check", "check-lun":
		if f.mode == "validator-replaced" {
			object, err := f.client.Tracker().Get(v1.SchemeGroupVersion.WithResource("pods"), ns, pod)
			if err != nil {
				return "", "", err
			}
			p := object.(*v1.Pod).DeepCopy()
			p.UID = "replacement"
			Expect(f.client.Tracker().Update(v1.SchemeGroupVersion.WithResource("pods"), p, ns)).To(Succeed())
		}
		if f.mode == "unobservable" {
			return "", "", probeError(2)
		}
		if f.mode == "residual" || (f.mode == "a-residual" && command == "check-lun") {
			return "", "", probeError(1)
		}
	case "seed":
		f.seed++
		if f.seed > 1 {
			return "", "", fmt.Errorf("reseed")
		}
	case "verify":
		if f.seed != 1 || f.mode == "payload" {
			return "", "", fmt.Errorf("payload mismatch")
		}
	case "progress":
		if f.mode != "stalled" {
			f.progress++
		}
		return strconv.Itoa(f.progress) + "\n", "", nil
	case "read":
		return SharedPayload, "", nil
	case "restart":
		object, err := f.client.Tracker().Get(v1.SchemeGroupVersion.WithResource("pods"), ns, pod)
		if err != nil {
			return "", "", err
		}
		p := object.(*v1.Pod).DeepCopy()
		p.Status.ContainerStatuses[0].RestartCount++
		if f.mode == "target-replaced" {
			p.UID = "replacement"
		}
		if f.mode == "target-moved" {
			p.Status.PodIP = "10.1.0.8"
		}
		Expect(f.client.Tracker().Update(v1.SchemeGroupVersion.WithResource("pods"), p, ns)).To(Succeed())
	}
	return "", "", nil
}

func (f *fixture) prepare()  { Expect(f.h.Prepare(context.Background())).To(Succeed()) }
func (f *fixture) baseline() { f.prepare(); Expect(f.h.Baseline(context.Background())).To(Succeed()) }
func created(actions []clienttesting.Action) bool {
	for _, a := range actions {
		if a.GetVerb() == "create" {
			return true
		}
	}
	return false
}

var _ = Describe("Typed Kubernetes lifecycle", func() {
	It("uses typed resources and Go commands through every supported scenario", func() {
		f := newFixture()
		f.baseline()
		for _, name := range []string{"republish", "driver-restart", "target-restart", "shared-target"} {
			By(name)
			Expect(f.h.RunScenario(context.Background(), name)).To(Succeed())
		}
		Expect(f.seed).To(Equal(1))
		Expect(f.progress).To(BeNumerically(">=", 4))
		Expect(f.h.Cleanup(context.Background())).To(Succeed())
		Expect(f.h.Cleanup(context.Background())).To(Succeed())
		_, err := f.client.CoreV1().Namespaces().Get(context.Background(), f.h.Config.Namespace, metav1.GetOptions{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		for _, action := range f.client.Actions() {
			if action.GetVerb() == "create" {
				switch object := action.(clienttesting.CreateAction).GetObject().(type) {
				case *v1.Pod:
					for _, c := range object.Spec.Containers {
						Expect(c.Command[0]).To(Equal("/e2e-tools"))
					}
				}
			}
		}
	})
	DescribeTable("refuses pre-existing resources before mutation", func(object func(*fixture) runtime.Object) {
		f := newFixture()
		Expect(f.client.Tracker().Add(object(f))).To(Succeed())
		Expect(Preflight(context.Background(), f.client, f.h.Config)).NotTo(Succeed())
		Expect(created(f.client.Actions())).To(BeFalse())
	},
		Entry("namespace", func(f *fixture) runtime.Object {
			return &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: f.h.Config.Namespace}}
		}),
		Entry("CSI registration", func(f *fixture) runtime.Object {
			return &storage.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: DriverName}}
		}),
		Entry("driver in another namespace", func(f *fixture) runtime.Object {
			return &apps.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "driver", Namespace: "other", Labels: map[string]string{"app": DriverDaemonSet}}}
		}),
		Entry("configmap", func(f *fixture) runtime.Object {
			return &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: DriverConfigMap, Namespace: DriverNamespace}}
		}),
		Entry("earlier run namespace", func(f *fixture) runtime.Object {
			return &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "old", Labels: map[string]string{OwnerLabel: f.h.Config.RunID}}}
		}),
		Entry("volume", func(f *fixture) runtime.Object {
			return &v1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: f.h.name("iscsi-pv")}}
		}),
	)
	It("propagates preflight observation failures without mutation", func() {
		f := newFixture()
		f.client.PrependReactor("list", "namespaces", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, fmt.Errorf("unavailable") })
		Expect(f.h.Prepare(context.Background())).NotTo(Succeed())
		Expect(created(f.client.Actions())).To(BeFalse())
	})
	It("does not adopt a namespace won by another invocation during create", func() {
		f := newFixture()
		f.client.PrependReactor("create", "namespaces", func(a clienttesting.Action) (bool, runtime.Object, error) {
			value := a.(clienttesting.CreateAction).GetObject().(*v1.Namespace).DeepCopy()
			value.Labels[InvocationLabel] = "other"
			Expect(f.client.Tracker().Add(value)).To(Succeed())
			return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "namespaces"}, value.Name)
		})
		Expect(f.h.Prepare(context.Background())).NotTo(Succeed())
		Expect(f.h.Cleanup(context.Background())).To(Succeed())
		_, err := f.client.CoreV1().Namespaces().Get(context.Background(), f.h.Config.Namespace, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
	})
	It("requires all validators and an image verifier before creating the target", func() {
		f := newFixture()
		f.h.VerifyTools = nil
		Expect(f.h.Prepare(context.Background())).To(MatchError(ContainSubstring("tools image identity")))
		sets, err := f.client.AppsV1().Deployments(f.h.Config.Namespace).List(context.Background(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(sets.Items).To(BeEmpty())
	})
	It("mounts the host read-only and probes without a shell", func() {
		f := newFixture()
		f.prepare()
		set, err := f.client.AppsV1().DaemonSets(f.h.Config.Namespace).Get(context.Background(), f.h.name("iscsi-init"), metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		pod := set.Spec.Template.Spec
		Expect(pod.HostPID).To(BeTrue())
		Expect(pod.Containers[0].VolumeMounts[0].ReadOnly).To(BeTrue())
		Expect(pod.Containers[0].ReadinessProbe.Exec.Command).To(Equal([]string{"/e2e-tools", "prerequisites", "--root=/host"}))
	})
	DescribeTable("retains supporting resources after node teardown cannot pass", func(mode string) {
		f := newFixture()
		f.baseline()
		f.h.Config.TeardownTimeout = 100 * time.Millisecond
		f.mode = mode
		Expect(f.h.Cleanup(context.Background())).NotTo(Succeed())
		for _, resource := range []string{"namespaces", "persistentvolumes", "persistentvolumeclaims", "deployments", "daemonsets", "csidrivers"} {
			for _, action := range f.client.Actions() {
				Expect(action.GetVerb() == "delete" && action.GetResource().Resource == resource).To(BeFalse(), "deleted supporting resource %s", resource)
			}
		}
	}, Entry("residual device", "residual"), Entry("unobservable state", "unobservable"))
	It("does cleanup with a fresh deadline after scenario cancellation", func() {
		f := newFixture()
		f.baseline()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		Expect(f.h.RunScenario(ctx, "republish")).NotTo(Succeed())
		Expect(f.h.Cleanup(ctx)).To(Succeed())
	})
	It("uses UID and resource-version delete preconditions", func() {
		f := newFixture()
		f.baseline()
		Expect(f.h.Cleanup(context.Background())).To(Succeed())
		for _, a := range f.client.Actions() {
			if a.GetVerb() == "delete" {
				options := a.(clienttesting.DeleteAction).GetDeleteOptions()
				Expect(options.Preconditions).NotTo(BeNil())
				Expect(options.Preconditions.UID).NotTo(BeNil())
				Expect(options.Preconditions.ResourceVersion).NotTo(BeNil())
			}
		}
	})
	It("preserves unrelated cluster volumes and driver-namespace objects", func() {
		f := newFixture()
		f.baseline()
		Expect(f.client.Tracker().Add(&v1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}})).To(Succeed())
		Expect(f.client.Tracker().Add(&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: DriverNamespace}})).To(Succeed())
		Expect(f.h.Cleanup(context.Background())).To(Succeed())
		_, err := f.client.CoreV1().PersistentVolumes().Get(context.Background(), "unrelated", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		_, err = f.client.CoreV1().ConfigMaps(DriverNamespace).Get(context.Background(), "unrelated", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
	})
	It("retains resources when a validator is absent", func() {
		f := newFixture()
		f.baseline()
		Expect(f.client.Tracker().Delete(v1.SchemeGroupVersion.WithResource("pods"), f.h.Config.Namespace, f.h.name("iscsi-init")+"-worker")).To(Succeed())
		Expect(f.h.Cleanup(context.Background())).To(MatchError(ContainSubstring("cannot snapshot")))
		Expect(f.calls).NotTo(ContainElement("delete:" + f.h.name("iscsi-pod")))
	})
	It("does not reseed or conceal payload failures", func() {
		f := newFixture()
		f.baseline()
		f.mode = "payload"
		Expect(f.h.RunScenario(context.Background(), "republish")).NotTo(Succeed())
		Expect(f.seed).To(Equal(1))
	})
	DescribeTable("requires the same target pod and address across container restart", func(mode string) {
		f := newFixture()
		f.baseline()
		f.mode = mode
		Expect(f.h.RunScenario(context.Background(), "target-restart")).NotTo(Succeed())
	}, Entry("pod replacement", "target-replaced"), Entry("address replacement", "target-moved"))
	It("requires an observed A device before testing shared removal", func() {
		f := newFixture()
		f.baseline()
		f.mode = "missing-device"
		Expect(f.h.RunScenario(context.Background(), "shared-target")).To(MatchError(ContainSubstring("no A LUN device")))
		Expect(f.calls).NotTo(ContainElement("delete:" + f.h.name("iscsi-pod")))
	})
	It("fails A removal even when B continues making progress", func() {
		f := newFixture()
		f.baseline()
		f.h.Config.TeardownTimeout = 100 * time.Millisecond
		f.mode = "a-residual"
		Expect(f.h.RunScenario(context.Background(), "shared-target")).To(MatchError(ContainSubstring("A removal was not verified")))
		Expect(f.progress).To(BeNumerically(">", 2))
	})
	It("bounds stalled B I/O before deleting A", func() {
		f := newFixture()
		f.baseline()
		f.h.Config.SharedIOTimeout = 35 * time.Millisecond
		f.mode = "stalled"
		f.progress = 1
		started := time.Now()
		Expect(f.h.RunScenario(context.Background(), "shared-target")).NotTo(Succeed())
		Expect(time.Since(started)).To(BeNumerically("<", f.h.Config.ScenarioTimeout))
		Expect(f.calls).NotTo(ContainElement("delete:" + f.h.name("iscsi-pod")))
	})
	It("arms teardown before an ambiguously accepted workload create", func() {
		f := newFixture()
		f.prepare()
		f.client.PrependReactor("create", "pods", func(a clienttesting.Action) (bool, runtime.Object, error) {
			p := a.(clienttesting.CreateAction).GetObject().(*v1.Pod)
			if p.Name == f.h.name("iscsi-pod") {
				Expect(f.client.Tracker().Add(p)).To(Succeed())
				return true, nil, fmt.Errorf("request timed out after admission")
			}
			return false, nil, nil
		})
		Expect(f.h.Baseline(context.Background())).NotTo(Succeed())
		f.mode = "unobservable"
		Expect(f.h.Cleanup(context.Background())).NotTo(Succeed())
		Expect(f.calls).To(ContainElement("exec:snapshot"))
	})
	It("rejects a validator replacement during a successful probe", func() {
		f := newFixture()
		f.baseline()
		f.mode = "validator-replaced"
		Expect(f.h.Cleanup(context.Background())).To(MatchError(ContainSubstring("validator changed")))
		_, err := f.client.AppsV1().Deployments(f.h.Config.Namespace).Get(context.Background(), f.h.name("iscsi-target"), metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
	})
	It("rejects a restarted B worker before deleting A", func() {
		f := newFixture()
		f.baseline()
		f.mode = "b-restarted"
		Expect(f.h.RunScenario(context.Background(), "shared-target")).To(MatchError(ContainSubstring("interrupted or restarted")))
		Expect(f.calls).NotTo(ContainElement("delete:" + f.h.name("iscsi-pod")))
	})
	It("does not equate an accepted namespace delete with cleanup completion", func() {
		f := newFixture()
		f.baseline()
		f.h.Config.TeardownTimeout = 100 * time.Millisecond
		f.client.PrependReactor("delete", "namespaces", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
		Expect(f.h.Cleanup(context.Background())).To(MatchError(context.DeadlineExceeded))
	})
	It("rejects invalid config before API operations", func() {
		f := newFixture()
		f.h.Config.RunID = "Bad_ID"
		Expect(f.h.Prepare(context.Background())).NotTo(Succeed())
		Expect(f.client.Actions()).To(BeEmpty())
	})
	It("fails when the publication namespace disappeared before verification", func() {
		f := newFixture()
		f.baseline()
		Expect(f.client.Tracker().Delete(v1.SchemeGroupVersion.WithResource("namespaces"), "", f.h.Config.Namespace)).To(Succeed())
		Expect(f.h.Cleanup(context.Background())).To(MatchError(ContainSubstring("cannot verify")))
	})
	It("fails without deleting resources if namespace ownership changes", func() {
		f := newFixture()
		f.baseline()
		object, err := f.client.Tracker().Get(v1.SchemeGroupVersion.WithResource("namespaces"), "", f.h.Config.Namespace)
		Expect(err).NotTo(HaveOccurred())
		ns := object.(*v1.Namespace).DeepCopy()
		ns.Labels[InvocationLabel] = "other"
		Expect(f.client.Tracker().Update(v1.SchemeGroupVersion.WithResource("namespaces"), ns, "")).To(Succeed())
		f.client.ClearActions()
		Expect(f.h.Cleanup(context.Background())).To(MatchError(ContainSubstring("ownership changed")))
		for _, a := range f.client.Actions() {
			Expect(a.GetVerb()).NotTo(Equal("delete"))
		}
	})
	DescribeTable("bounds A removal API calls by B's progress deadline", func(verb string) {
		f := newFixture()
		f.baseline()
		intercepted := false
		f.h.Client = inspectClient{Interface: f.client, inspect: func(ctx context.Context, operation, name string) error {
			if operation == verb && name == f.h.name("iscsi-pod") && f.progress >= 2 {
				intercepted = true
				deadline, ok := ctx.Deadline()
				Expect(ok).To(BeTrue())
				Expect(time.Until(deadline)).To(BeNumerically("<=", f.h.Config.SharedIOTimeout))
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}}
		started := time.Now()
		Expect(f.h.RunScenario(context.Background(), "shared-target")).To(MatchError(ContainSubstring("A removal was not verified")))
		Expect(intercepted).To(BeTrue())
		Expect(time.Since(started)).To(BeNumerically("<", f.h.Config.ScenarioTimeout))
	}, Entry("GET", "get"), Entry("DELETE", "delete"))
	It("still waits for cluster resource deletion when the namespace disappears during cleanup", func() {
		f := newFixture()
		f.baseline()
		f.h.Config.TeardownTimeout = 100 * time.Millisecond
		gets := 0
		f.client.PrependReactor("get", "namespaces", func(a clienttesting.Action) (bool, runtime.Object, error) {
			gets++
			if gets >= 2 {
				return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, f.h.Config.Namespace)
			}
			return false, nil, nil
		})
		f.client.PrependReactor("delete", "persistentvolumes", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
		Expect(f.h.Cleanup(context.Background())).To(MatchError(context.DeadlineExceeded))
	})
	It("requires both shared-target publications to be on the same node", func() {
		f := newFixture()
		f.baseline()
		f.mode = "b-different-node"
		Expect(f.h.RunScenario(context.Background(), "shared-target")).To(MatchError(ContainSubstring("on A's node")))
		Expect(f.calls).NotTo(ContainElement("delete:" + f.h.name("iscsi-pod")))
	})
	It("re-reads a PV after a controller changes its resource version before deletion", func() {
		f := newFixture()
		f.baseline()
		attempts := 0
		var uid types.UID
		f.client.PrependReactor("delete", "persistentvolumes", func(action clienttesting.Action) (bool, runtime.Object, error) {
			a := action.(clienttesting.DeleteAction)
			attempts++
			options := a.GetDeleteOptions()
			object, err := f.client.Tracker().Get(v1.SchemeGroupVersion.WithResource("persistentvolumes"), "", a.GetName())
			Expect(err).NotTo(HaveOccurred())
			volume := object.(*v1.PersistentVolume).DeepCopy()
			if attempts == 1 {
				uid = volume.UID
				Expect(*options.Preconditions.UID).To(Equal(uid))
				Expect(*options.Preconditions.ResourceVersion).To(Equal("1"))
				volume.ResourceVersion = "2"
				Expect(f.client.Tracker().Update(v1.SchemeGroupVersion.WithResource("persistentvolumes"), volume, "")).To(Succeed())
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "persistentvolumes"}, volume.Name, fmt.Errorf("binder updated status"))
			}
			Expect(*options.Preconditions.UID).To(Equal(uid))
			Expect(*options.Preconditions.ResourceVersion).To(Equal("2"))
			return false, nil, nil
		})
		Expect(f.h.Cleanup(context.Background())).To(Succeed())
		Expect(attempts).To(Equal(2))
	})
	DescribeTable("refuses to retry a conflicted delete after identity or ownership changes", func(change string) {
		f := newFixture()
		f.baseline()
		attempts := 0
		f.client.PrependReactor("delete", "persistentvolumes", func(action clienttesting.Action) (bool, runtime.Object, error) {
			a := action.(clienttesting.DeleteAction)
			attempts++
			object, err := f.client.Tracker().Get(v1.SchemeGroupVersion.WithResource("persistentvolumes"), "", a.GetName())
			Expect(err).NotTo(HaveOccurred())
			volume := object.(*v1.PersistentVolume).DeepCopy()
			volume.ResourceVersion = "2"
			switch change {
			case "uid":
				volume.UID = "replacement"
			case "run":
				volume.Labels[OwnerLabel] = "other"
			case "invocation":
				volume.Labels[InvocationLabel] = "other"
			}
			Expect(f.client.Tracker().Update(v1.SchemeGroupVersion.WithResource("persistentvolumes"), volume, "")).To(Succeed())
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "persistentvolumes"}, volume.Name, fmt.Errorf("changed"))
		})
		Expect(f.h.Cleanup(context.Background())).To(MatchError(ContainSubstring("identity or ownership changed")))
		Expect(attempts).To(Equal(1))
		_, err := f.client.CoreV1().PersistentVolumes().Get(context.Background(), f.h.name("iscsi-pv"), metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
	}, Entry("UID", "uid"), Entry("run label", "run"), Entry("invocation label", "invocation"))
	It("bounds persistent delete conflicts by the cleanup deadline", func() {
		f := newFixture()
		f.baseline()
		f.h.Config.TeardownTimeout = 100 * time.Millisecond
		attempts := 0
		f.client.PrependReactor("delete", "persistentvolumes", func(action clienttesting.Action) (bool, runtime.Object, error) {
			attempts++
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "persistentvolumes"}, action.(clienttesting.DeleteAction).GetName(), fmt.Errorf("controller busy"))
		})
		started := time.Now()
		Expect(f.h.Cleanup(context.Background())).To(MatchError(context.DeadlineExceeded))
		Expect(attempts).To(BeNumerically(">", 1))
		Expect(time.Since(started)).To(BeNumerically("<", f.h.Config.ScenarioTimeout))
		_, err := f.client.AppsV1().Deployments(f.h.Config.Namespace).Get(context.Background(), f.h.name("iscsi-target"), metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
	})
	It("refreshes namespace deletion preconditions after a status update", func() {
		f := newFixture()
		f.baseline()
		attempts := 0
		f.client.PrependReactor("delete", "namespaces", func(action clienttesting.Action) (bool, runtime.Object, error) {
			a := action.(clienttesting.DeleteAction)
			attempts++
			if attempts == 1 {
				object, err := f.client.Tracker().Get(v1.SchemeGroupVersion.WithResource("namespaces"), "", a.GetName())
				Expect(err).NotTo(HaveOccurred())
				ns := object.(*v1.Namespace).DeepCopy()
				ns.ResourceVersion = "2"
				Expect(f.client.Tracker().Update(v1.SchemeGroupVersion.WithResource("namespaces"), ns, "")).To(Succeed())
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, ns.Name, fmt.Errorf("status update"))
			}
			Expect(*a.GetDeleteOptions().Preconditions.ResourceVersion).To(Equal("2"))
			return false, nil, nil
		})
		Expect(f.h.Cleanup(context.Background())).To(Succeed())
		Expect(attempts).To(Equal(2))
	})
})
