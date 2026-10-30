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
	"errors"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type validatorState struct {
	Pod     v1.Pod
	Devices string
}

func deletion(object metav1.Object) metav1.DeleteOptions {
	uid, version := object.GetUID(), object.GetResourceVersion()
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}
}

// Controllers can change status (and resourceVersion) after a list/get. Retry
// only conflicts, re-reading the object and retaining its original identity.
// A reused name or changed ownership must never turn into a delete retry.
func deleteOwned[T metav1.Object](ctx context.Context, h *Harness, original T,
	get func(context.Context, string, metav1.GetOptions) (T, error),
	remove func(context.Context, string, metav1.DeleteOptions) error,
) error {
	uid, name, namespace := original.GetUID(), original.GetName(), original.GetNamespace()
	current, refresh := original, false
	return h.poll(ctx, func(ctx context.Context) (bool, error) {
		if refresh {
			value, err := get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			current = value
		}
		if uid == "" || current.GetUID() != uid || current.GetName() != name || current.GetNamespace() != namespace || !h.owned(current.GetLabels()) {
			return false, fmt.Errorf("resource identity or ownership changed; refusing deletion")
		}
		if current.GetResourceVersion() == "" {
			return false, fmt.Errorf("resource version is unobservable; refusing deletion")
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		err := remove(ctx, name, deletion(current))
		if apierrors.IsConflict(err) {
			refresh = true
			return false, nil
		}
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return true, ctx.Err()
	})
}

func (h *Harness) validators(ctx context.Context) ([]v1.Pod, error) {
	set, err := h.Client.AppsV1().DaemonSets(h.Config.Namespace).Get(ctx, h.name("iscsi-init"), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if !h.owned(set.Labels) || set.Status.DesiredNumberScheduled < 1 {
		return nil, fmt.Errorf("cannot verify teardown without every node validator ready")
	}
	pods, err := h.Client.CoreV1().Pods(h.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + h.name("iscsi-init")})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) != int(set.Status.DesiredNumberScheduled) {
		return nil, fmt.Errorf("cannot verify teardown without every node validator ready")
	}
	nodes := map[string]bool{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.UID == "" || !h.owned(p.Labels) || !readyPod(p) || p.Spec.NodeName == "" || nodes[p.Spec.NodeName] {
			return nil, fmt.Errorf("cannot verify teardown without every node validator ready")
		}
		nodes[p.Spec.NodeName] = true
	}
	return pods.Items, nil
}

func (h *Harness) snapshot(ctx context.Context, mode, volume, lun string) ([]validatorState, error) {
	pods, err := h.validators(ctx)
	if err != nil {
		return nil, err
	}
	states := []validatorState{}
	for _, pod := range pods {
		args := []string{"/e2e-tools", mode, "--root=/host", "--iqn=" + h.Config.IQN, "--volume=" + volume}
		if lun != "" {
			args = append(args, "--lun="+lun)
		}
		devices, err := h.exec(ctx, pod.Name, "prerequisites", args...)
		if err != nil {
			return nil, err
		}
		states = append(states, validatorState{Pod: pod, Devices: strings.TrimSpace(devices)})
	}
	return states, nil
}

type exitStatus interface{ ExitStatus() int }
type exitCode interface{ ExitCode() int }

func residual(err error) bool {
	var status exitStatus
	if errors.As(err, &status) {
		return status.ExitStatus() == 1
	}
	var code exitCode
	if errors.As(err, &code) {
		return code.ExitCode() == 1
	}
	return false
}

func (h *Harness) observedClean(ctx context.Context, states []validatorState, mode, volume, lun string) (bool, error) {
	clean := true
	for _, state := range states {
		current, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, state.Pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.UID != state.Pod.UID || !h.owned(current.Labels) || !readyPod(current) {
			return false, fmt.Errorf("node validator changed during teardown")
		}
		args := []string{"/e2e-tools", mode, "--root=/host", "--iqn=" + h.Config.IQN, "--volume=" + volume, "--tracked=" + state.Devices}
		if lun != "" {
			args = append(args, "--lun="+lun)
		}
		if _, err = h.exec(ctx, state.Pod.Name, "prerequisites", args...); err != nil {
			if residual(err) {
				clean = false
			} else {
				return false, err
			}
		}
		after, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, state.Pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if after.UID != state.Pod.UID || !h.owned(after.Labels) || !readyPod(after) {
			return false, fmt.Errorf("node validator changed during observation")
		}
	}
	return clean, nil
}

func (h *Harness) deletePod(ctx context.Context, name string, wait bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pod, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !h.owned(pod.Labels) {
		return fmt.Errorf("refusing to delete unowned pod")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pods := h.Client.CoreV1().Pods(h.Config.Namespace)
	if err = deleteOwned(ctx, h, pod, pods.Get, pods.Delete); err != nil {
		return err
	}
	if !wait {
		return ctx.Err()
	}
	return h.poll(ctx, func(ctx context.Context) (bool, error) {
		current, e := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(e) {
			return true, nil
		}
		if e != nil {
			return false, e
		}
		if current.UID != pod.UID {
			return false, fmt.Errorf("pod unexpectedly replaced during deletion")
		}
		return false, nil
	})
}

// Cleanup intentionally uses a fresh budget: a canceled scenario must still
// perform safe cleanup. Supporting resources survive unobservable teardown.
func (h *Harness) Cleanup(_ context.Context) error {
	if h.cleaned {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.Config.TeardownTimeout)
	defer cancel()
	namespace, err := h.Client.CoreV1().Namespaces().Get(ctx, h.Config.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if h.published || h.createdNamespace {
			return fmt.Errorf("publication namespace disappeared; cannot verify node teardown")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !h.owned(namespace.Labels) {
		if h.published || h.createdNamespace {
			return fmt.Errorf("namespace ownership changed; retaining resources because teardown cannot be verified")
		}
		return nil
	}
	var states []validatorState
	if h.published {
		states, err = h.snapshot(ctx, "snapshot", h.name("iscsi-pv"), "")
		if err != nil {
			return fmt.Errorf("cannot snapshot node devices; retaining supporting resources: %w", err)
		}
	}
	for _, prefix := range []string{"iscsi-pod", "iscsi-pod-b", "iscsi-check"} {
		if err = h.deletePod(ctx, h.name(prefix), true); err != nil {
			return fmt.Errorf("workload deletion failed; retaining supporting resources: %w", err)
		}
	}
	if h.published {
		volumes := []string{h.name("iscsi-pv")}
		if h.Config.LUNCount == 2 {
			volumes = append(volumes, h.name("iscsi-pv-b"))
		}
		for _, volume := range volumes {
			if err = h.poll(ctx, func(ctx context.Context) (bool, error) { return h.observedClean(ctx, states, "check", volume, "") }); err != nil {
				return fmt.Errorf("node teardown failed; retaining target, driver, claims and namespace: %w", err)
			}
		}
	}
	if err = h.removeSupportingResources(ctx); err != nil {
		return err
	}
	current, err := h.Client.CoreV1().Namespaces().Get(ctx, h.Config.Namespace, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if !h.owned(current.Labels) || current.UID != namespace.UID {
			return fmt.Errorf("namespace ownership changed; refusing deletion")
		}
		namespaces := h.Client.CoreV1().Namespaces()
		if err = deleteOwned(ctx, h, current, namespaces.Get, namespaces.Delete); err != nil {
			return err
		}
	}
	if err = h.waitRemoved(ctx); err != nil {
		return err
	}
	h.cleaned = true
	return nil
}

// Delete acceptance is not deletion completion: observe finalizers and the
// driver controller's pods before reporting a completely cleaned test run.
func (h *Harness) waitRemoved(ctx context.Context) error {
	return h.poll(ctx, func(ctx context.Context) (bool, error) {
		if _, err := h.Client.CoreV1().Namespaces().Get(ctx, h.Config.Namespace, metav1.GetOptions{}); err == nil {
			return false, nil
		} else if !apierrors.IsNotFound(err) {
			return false, err
		}
		options := metav1.ListOptions{LabelSelector: h.selector()}
		volumes, err := h.Client.CoreV1().PersistentVolumes().List(ctx, options)
		if err != nil {
			return false, err
		}
		drivers, err := h.Client.StorageV1().CSIDrivers().List(ctx, options)
		if err != nil {
			return false, err
		}
		sets, err := h.Client.AppsV1().DaemonSets(DriverNamespace).List(ctx, options)
		if err != nil {
			return false, err
		}
		maps, err := h.Client.CoreV1().ConfigMaps(DriverNamespace).List(ctx, options)
		if err != nil {
			return false, err
		}
		pods, err := h.Client.CoreV1().Pods(DriverNamespace).List(ctx, options)
		if err != nil {
			return false, err
		}
		return len(volumes.Items)+len(drivers.Items)+len(sets.Items)+len(maps.Items)+len(pods.Items) == 0, nil
	})
}

func (h *Harness) removeSupportingResources(ctx context.Context) error {
	options := metav1.ListOptions{LabelSelector: h.selector()}
	claims, err := h.Client.CoreV1().PersistentVolumeClaims(h.Config.Namespace).List(ctx, options)
	if err != nil {
		return err
	}
	for i := range claims.Items {
		v := &claims.Items[i]
		if !h.owned(v.Labels) {
			continue
		}
		resources := h.Client.CoreV1().PersistentVolumeClaims(h.Config.Namespace)
		if err = deleteOwned(ctx, h, v, resources.Get, resources.Delete); err != nil {
			return err
		}
	}
	volumes, err := h.Client.CoreV1().PersistentVolumes().List(ctx, options)
	if err != nil {
		return err
	}
	for i := range volumes.Items {
		v := &volumes.Items[i]
		if !h.owned(v.Labels) {
			continue
		}
		resources := h.Client.CoreV1().PersistentVolumes()
		if err = deleteOwned(ctx, h, v, resources.Get, resources.Delete); err != nil {
			return err
		}
	}
	deployments, err := h.Client.AppsV1().Deployments(h.Config.Namespace).List(ctx, options)
	if err != nil {
		return err
	}
	for i := range deployments.Items {
		v := &deployments.Items[i]
		if !h.owned(v.Labels) {
			continue
		}
		resources := h.Client.AppsV1().Deployments(h.Config.Namespace)
		if err = deleteOwned(ctx, h, v, resources.Get, resources.Delete); err != nil {
			return err
		}
	}
	services, err := h.Client.CoreV1().Services(h.Config.Namespace).List(ctx, options)
	if err != nil {
		return err
	}
	for i := range services.Items {
		v := &services.Items[i]
		if !h.owned(v.Labels) {
			continue
		}
		resources := h.Client.CoreV1().Services(h.Config.Namespace)
		if err = deleteOwned(ctx, h, v, resources.Get, resources.Delete); err != nil {
			return err
		}
	}
	for _, namespace := range []string{h.Config.Namespace, DriverNamespace} {
		sets, err := h.Client.AppsV1().DaemonSets(namespace).List(ctx, options)
		if err != nil {
			return err
		}
		for i := range sets.Items {
			v := &sets.Items[i]
			if !h.owned(v.Labels) {
				continue
			}
			resources := h.Client.AppsV1().DaemonSets(namespace)
			if err = deleteOwned(ctx, h, v, resources.Get, resources.Delete); err != nil {
				return err
			}
		}
		maps, err := h.Client.CoreV1().ConfigMaps(namespace).List(ctx, options)
		if err != nil {
			return err
		}
		for i := range maps.Items {
			v := &maps.Items[i]
			if !h.owned(v.Labels) {
				continue
			}
			resources := h.Client.CoreV1().ConfigMaps(namespace)
			if err = deleteOwned(ctx, h, v, resources.Get, resources.Delete); err != nil {
				return err
			}
		}
	}
	drivers, err := h.Client.StorageV1().CSIDrivers().List(ctx, options)
	if err != nil {
		return err
	}
	for i := range drivers.Items {
		v := &drivers.Items[i]
		if !h.owned(v.Labels) {
			continue
		}
		resources := h.Client.StorageV1().CSIDrivers()
		if err = deleteOwned(ctx, h, v, resources.Get, resources.Delete); err != nil {
			return err
		}
	}
	return nil
}
