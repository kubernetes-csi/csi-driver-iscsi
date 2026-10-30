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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	OwnerLabel      = "iscsi.csi.k8s.io/e2e-run"
	InvocationLabel = "iscsi.csi.k8s.io/e2e-invocation"
	DriverName      = "iscsi.csi.k8s.io"
	DriverNamespace = "kube-system"
	DriverDaemonSet = "csi-iscsi-node"
	DriverConfigMap = "configmap-csi-iscsiadm"
	Payload         = "iSCSI CSI test successful"
	SharedPayload   = "iSCSI shared target B payload\n"
)

type Config struct {
	RunID, Namespace, Token, IQN, RepoRoot                                            string
	DriverImage, TargetImage, ToolsImage                                              string
	DriverPullPolicy, TargetPullPolicy, ToolsPullPolicy                               v1.PullPolicy
	CommandTimeout, RequestTimeout, ScenarioTimeout, TeardownTimeout, SharedIOTimeout time.Duration
	RetainOnFailure                                                                   bool
	LUNCount                                                                          int
}

func NewIdentity() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func (c Config) Defaults() Config {
	if c.Namespace == "" {
		c.Namespace = "iscsi-e2e-" + c.RunID
	}
	if c.IQN == "" {
		c.IQN = "iqn.2026-01.com.test:storage-" + c.RunID
	}
	if c.CommandTimeout == 0 {
		c.CommandTimeout = 330 * time.Second
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 30 * time.Second
	}
	if c.ScenarioTimeout == 0 {
		c.ScenarioTimeout = 180 * time.Second
	}
	if c.TeardownTimeout == 0 {
		c.TeardownTimeout = 60 * time.Second
	}
	if c.SharedIOTimeout == 0 {
		c.SharedIOTimeout = 10 * time.Second
	}
	if c.LUNCount == 0 {
		c.LUNCount = 1
	}
	return c
}

func (c Config) Validate() error {
	if !regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,18}[a-z0-9])?$`).MatchString(c.RunID) {
		return fmt.Errorf("E2E_RUN_ID must be 1-20 lowercase alphanumeric/hyphen characters")
	}
	if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`).MatchString(c.Namespace) {
		return fmt.Errorf("NAMESPACE must be a DNS label (at most 63 characters)")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9-]{1,63}$`).MatchString(c.Token) {
		return fmt.Errorf("a unique invocation token is required")
	}
	for _, duration := range []time.Duration{c.CommandTimeout, c.RequestTimeout, c.ScenarioTimeout, c.TeardownTimeout, c.SharedIOTimeout} {
		if duration <= 0 {
			return fmt.Errorf("E2E timeouts must be positive")
		}
	}
	if c.LUNCount != 1 && c.LUNCount != 2 {
		return fmt.Errorf("LUNCount must be 1 or 2")
	}
	return nil
}

type PodExecutor interface {
	Exec(context.Context, string, string, string, []string) (string, string, error)
}

type Harness struct {
	Client           kubernetes.Interface
	Executor         PodExecutor
	Config           Config
	VerifyDriver     func(context.Context, []v1.Pod) error
	VerifyTarget     func(context.Context, *v1.Pod) error
	VerifyTools      func(context.Context, []v1.Pod) error
	PollInterval     time.Duration
	portal           string
	published        bool
	createdNamespace bool
	cleaned          bool
}

func New(client kubernetes.Interface, executor PodExecutor, cfg Config) *Harness {
	return &Harness{Client: client, Executor: executor, Config: cfg.Defaults(), PollInterval: time.Second}
}

func (h *Harness) name(prefix string) string { return prefix + "-" + h.Config.RunID }
func (h *Harness) labels() map[string]string {
	return map[string]string{OwnerLabel: h.Config.RunID, InvocationLabel: h.Config.Token}
}
func (h *Harness) selector() string {
	return OwnerLabel + "=" + h.Config.RunID + "," + InvocationLabel + "=" + h.Config.Token
}
func (h *Harness) owned(labels map[string]string) bool {
	return labels[OwnerLabel] == h.Config.RunID && labels[InvocationLabel] == h.Config.Token
}

func (h *Harness) operation(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, h.Config.CommandTimeout)
}

func (h *Harness) exec(ctx context.Context, pod, container string, args ...string) (string, error) {
	if h.Executor == nil {
		return "", fmt.Errorf("pod executor is required")
	}
	ctx, cancel := h.operation(ctx)
	defer cancel()
	stdout, _, err := h.Executor.Exec(ctx, h.Config.Namespace, pod, container, args)
	if err != nil {
		return "", fmt.Errorf("pod command failed: %w", err)
	}
	return stdout, nil
}

// Poll checks immediately, and every operation consumes the caller's deadline.
func (h *Harness) poll(ctx context.Context, fn func(context.Context) (bool, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := fn(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		timer := time.NewTimer(h.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
