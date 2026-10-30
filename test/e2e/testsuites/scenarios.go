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
	"net"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (h *Harness) Baseline(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, h.Config.ScenarioTimeout)
	defer cancel()
	if err := h.createVolume(ctx, "", 1, "", false); err != nil {
		return err
	}
	if _, err := h.exec(ctx, h.name("iscsi-pod"), "test", "/e2e-tools", "seed", "--path=/mnt/test-file", "--payload="+Payload); err != nil {
		return err
	}
	return h.verifyPayload(ctx)
}

func (h *Harness) verifyPayload(ctx context.Context) error {
	_, err := h.exec(ctx, h.name("iscsi-pod"), "test", "/e2e-tools", "verify", "--path=/mnt/test-file", "--payload="+Payload)
	return err
}

func (h *Harness) RunScenario(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, h.Config.ScenarioTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	switch name {
	case "baseline":
		return h.verifyPayload(ctx)
	case "republish":
		err = h.republish(ctx)
	case "driver-restart":
		err = h.restartDriver(ctx)
	case "target-restart":
		err = h.restartTarget(ctx)
	case "shared-target":
		err = h.sharedTarget(ctx)
	default:
		return fmt.Errorf("unknown executable scenario %q", name)
	}
	if err != nil {
		return err
	}
	return h.verifyPayload(ctx)
}

func (h *Harness) republish(ctx context.Context) error {
	previous, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, h.name("iscsi-pod"), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if previous.UID == "" || !h.owned(previous.Labels) {
		return fmt.Errorf("cannot identify owned publication pod")
	}
	if err = h.deletePod(ctx, previous.Name, true); err != nil {
		return err
	}
	if err = h.createWorkload(ctx, previous.Name, h.name("iscsi-pvc"), "", false); err != nil {
		return err
	}
	current, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, previous.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.UID == "" || current.UID == previous.UID {
		return fmt.Errorf("publication pod was not replaced")
	}
	return nil
}

func (h *Harness) restartDriver(ctx context.Context) error {
	previous, err := h.Client.CoreV1().Pods(DriverNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + DriverDaemonSet})
	if err != nil {
		return err
	}
	if len(previous.Items) == 0 {
		return fmt.Errorf("no driver pods to restart")
	}
	nodes := map[string]bool{}
	uids := map[string]bool{}
	for i := range previous.Items {
		pod := &previous.Items[i]
		if !h.owned(pod.Labels) || pod.UID == "" || pod.Spec.NodeName == "" {
			return fmt.Errorf("cannot identify owned driver pods")
		}
		nodes[pod.Spec.NodeName] = true
		uids[string(pod.UID)] = true
		if err = h.Client.CoreV1().Pods(DriverNamespace).Delete(ctx, pod.Name, deletion(pod)); err != nil {
			return err
		}
	}
	if err = h.poll(ctx, func(ctx context.Context) (bool, error) {
		current, err := h.Client.CoreV1().Pods(DriverNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + DriverDaemonSet})
		if err != nil {
			return false, err
		}
		if len(current.Items) != len(previous.Items) {
			return false, nil
		}
		seen := map[string]bool{}
		for i := range current.Items {
			p := &current.Items[i]
			if p.UID == "" || uids[string(p.UID)] || !readyPod(p) {
				return false, nil
			}
			if !h.owned(p.Labels) || !nodes[p.Spec.NodeName] || seen[p.Spec.NodeName] {
				return false, fmt.Errorf("driver restart changed node coverage")
			}
			seen[p.Spec.NodeName] = true
		}
		return len(seen) == len(nodes), nil
	}); err != nil {
		return err
	}
	return h.verifyDriver(ctx)
}

func (h *Harness) restartTarget(ctx context.Context) error {
	previous, err := h.targetPod(ctx)
	if err != nil {
		return err
	}
	status, ok := containerStatus(previous, "tgtd")
	if !ok {
		return fmt.Errorf("cannot identify target container")
	}
	// The target helper signals its own supervisor. Its exec stream may close
	// when PID1 exits; a verified same-pod restart below is the success criterion.
	_, _ = h.exec(ctx, previous.Name, "tgtd", "/tgtd-fixture", "restart")
	if err = h.poll(ctx, func(ctx context.Context) (bool, error) {
		current, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, previous.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.UID != previous.UID || current.Status.PodIP != previous.Status.PodIP || !h.owned(current.Labels) {
			return false, fmt.Errorf("target restart replaced pod or address")
		}
		currentStatus, found := containerStatus(current, "tgtd")
		return found && readyPod(current) && currentStatus.Ready && currentStatus.RestartCount > status.RestartCount, nil
	}); err != nil {
		return err
	}
	current, err := h.targetPod(ctx)
	if err != nil {
		return err
	}
	if net.JoinHostPort(current.Status.PodIP, "3260") != h.portal {
		return fmt.Errorf("target endpoint changed")
	}
	return nil
}

func containerStatus(pod *v1.Pod, name string) (v1.ContainerStatus, bool) {
	for _, s := range pod.Status.ContainerStatuses {
		if s.Name == name {
			return s, true
		}
	}
	return v1.ContainerStatus{}, false
}

type sharedProgress struct {
	Pod      v1.Pod
	Count    int64
	Advanced time.Time
}

func (h *Harness) observeShared(ctx context.Context, state *sharedProgress) error {
	pod, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, state.Pod.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	status, found := containerStatus(pod, "test")
	if pod.UID != state.Pod.UID || pod.Spec.NodeName != state.Pod.Spec.NodeName || !h.owned(pod.Labels) || !readyPod(pod) || !found || !status.Ready || status.RestartCount != 0 || status.State.Running == nil {
		return fmt.Errorf("shared target B worker interrupted or restarted")
	}
	output, err := h.exec(ctx, pod.Name, "test", "/e2e-tools", "progress", "--path=/tmp/io-progress")
	if err != nil {
		return err
	}
	text := strings.TrimRight(output, "\n")
	if text == "" || text[0] < '1' || text[0] > '9' {
		return fmt.Errorf("shared target B progress is invalid")
	}
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("shared target B progress is invalid")
		}
	}
	count, err := strconv.ParseInt(text, 10, 64)
	if err != nil || count < 1 || count < state.Count {
		return fmt.Errorf("shared target B progress is invalid or reset")
	}
	if count > state.Count {
		state.Count = count
		state.Advanced = time.Now()
	}
	if time.Since(state.Advanced) >= h.Config.SharedIOTimeout {
		return fmt.Errorf("shared target B checked I/O stopped advancing")
	}
	return nil
}

func (h *Harness) sharedTarget(ctx context.Context) error {
	if h.Config.LUNCount != 2 {
		return fmt.Errorf("shared target scenario requires two LUNs")
	}
	a, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, h.name("iscsi-pod"), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if a.UID == "" || a.Spec.NodeName == "" || !h.owned(a.Labels) {
		return fmt.Errorf("cannot identify shared target A node/pod")
	}
	if err = h.createVolume(ctx, "-b", 2, a.Spec.NodeName, true); err != nil {
		return err
	}
	b, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, h.name("iscsi-pod-b"), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if b.UID == "" || b.Spec.NodeName != a.Spec.NodeName || !h.owned(b.Labels) || !readyPod(b) {
		return fmt.Errorf("cannot identify shared target B worker on A's node")
	}
	state := &sharedProgress{Pod: *b, Advanced: time.Now()}
	err = h.poll(ctx, func(ctx context.Context) (bool, error) {
		operation, stop := context.WithDeadline(ctx, state.Advanced.Add(h.Config.SharedIOTimeout))
		defer stop()
		if e := h.observeShared(operation, state); e != nil {
			return false, e
		}
		return state.Count >= 2, nil
	})
	if err != nil {
		return err
	}
	baseline := state.Count
	snapshotCtx, snapshotCancel := context.WithDeadline(ctx, state.Advanced.Add(h.Config.SharedIOTimeout))
	defer snapshotCancel()
	states, err := h.snapshot(snapshotCtx, "snapshot-lun", h.name("iscsi-pv"), "1")
	if err != nil {
		return err
	}
	observed := 0
	for _, validator := range states {
		if validator.Pod.Spec.NodeName == a.Spec.NodeName {
			if _, err = h.exec(snapshotCtx, validator.Pod.Name, "prerequisites", "/e2e-tools", "published", "--root=/host", "--iqn="+h.Config.IQN, "--volume="+h.name("iscsi-pv")); err != nil {
				return err
			}
			if validator.Devices == "" {
				return fmt.Errorf("no A LUN device observed; cannot prove removal")
			}
			observed++
		}
	}
	if observed != 1 {
		return fmt.Errorf("cannot observe A publication on its node")
	}
	removeCtx, removeCancel := context.WithTimeout(ctx, h.Config.TeardownTimeout)
	defer removeCancel()
	deleteCtx, deleteCancel := context.WithDeadline(removeCtx, state.Advanced.Add(h.Config.SharedIOTimeout))
	err = h.deletePod(deleteCtx, a.Name, false)
	deleteCancel()
	if err != nil {
		return fmt.Errorf("shared-target A removal was not verified (S06/S07 gate): %w", err)
	}
	err = h.poll(removeCtx, func(ctx context.Context) (bool, error) {
		deadline := state.Advanced.Add(h.Config.SharedIOTimeout)
		operation, stop := context.WithDeadline(ctx, deadline)
		if err := h.observeShared(operation, state); err != nil {
			stop()
			return false, err
		}
		stop()
		operation, stop = context.WithDeadline(ctx, state.Advanced.Add(h.Config.SharedIOTimeout))
		defer stop()
		current, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(operation, a.Name, metav1.GetOptions{})
		gone := apierrors.IsNotFound(err)
		if err != nil && !gone {
			return false, err
		}
		if !gone && current.UID != a.UID {
			return false, fmt.Errorf("publication A was unexpectedly replaced")
		}
		clean, err := h.observedClean(operation, states, "check-lun", h.name("iscsi-pv"), "1")
		if err != nil {
			return false, err
		}
		return gone && clean && state.Count >= baseline+2, nil
	})
	if err != nil {
		return fmt.Errorf("shared-target A removal was not verified (S06/S07 gate): %w", err)
	}
	if err = h.createWorkload(ctx, a.Name, h.name("iscsi-pvc"), "", false); err != nil {
		return err
	}
	current, err := h.Client.CoreV1().Pods(h.Config.Namespace).Get(ctx, a.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.UID == "" || current.UID == a.UID {
		return fmt.Errorf("publication A was not republished")
	}
	if err = h.observeShared(ctx, state); err != nil {
		return err
	}
	output, err := h.exec(ctx, b.Name, "test", "/e2e-tools", "read", "--path=/mnt/b-pattern")
	if err != nil {
		return err
	}
	if output != SharedPayload {
		return fmt.Errorf("shared target B data changed")
	}
	return nil
}
