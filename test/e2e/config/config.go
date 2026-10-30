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

package config

import (
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/images"
	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/testsuites"
)

type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Select validates the entire request before reporting a known feature gate.
// No executable subset of a gated request may mutate a cluster.
func Select(value string) ([]string, error) {
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return nil, &Error{2, "E2E_SCENARIOS must be a comma-separated list without whitespace"}
	}
	if value == "" || strings.HasSuffix(value, ",") {
		return nil, &Error{2, "E2E_SCENARIOS must not be empty"}
	}
	if value == "all" {
		return []string{"republish", "driver-restart", "target-restart"}, nil
	}
	known := map[string]string{"baseline": "", "republish": "", "driver-restart": "", "target-restart": "", "shared-target": "",
		"chap-secret-ref": "nodePublishSecretRef support is incomplete", "stats": "NodeGetVolumeStats is not implemented",
		"raw-block": "raw block publication is not implemented", "expansion": "NodeExpandVolume is not implemented", "multipath": "distinct paths have not been validated"}
	selected := strings.Split(value, ",")
	for _, name := range selected {
		if _, ok := known[name]; !ok {
			return nil, &Error{2, "Unknown E2E_SCENARIOS entry: " + name}
		}
	}
	for _, name := range selected {
		if reason := known[name]; reason != "" {
			return nil, &Error{78, "Feature gate " + name + ": " + reason + ". No scenarios ran"}
		}
	}
	return selected, nil
}

type Runtime struct {
	Harness                                    testsuites.Config
	Images                                     images.Options
	DriverTag, TargetTag, ToolsTag, Kubeconfig string
	Scenarios                                  []string
}

func FromEnvironment(lookup func(string) (string, bool), repoRoot string) (Runtime, error) {
	var result Runtime
	get := func(name string) string { value, _ := lookup(name); return value }
	selection, present := lookup("E2E_SCENARIOS")
	if !present {
		selection = "baseline"
	}
	var err error
	result.Scenarios, err = Select(selection)
	if err != nil {
		return result, err
	}
	id := get("E2E_RUN_ID")
	if id == "" {
		id, err = testsuites.NewIdentity()
		if err != nil {
			return result, err
		}
		id = id[:12]
	}
	token, err := testsuites.NewIdentity()
	if err != nil {
		return result, err
	}
	cfg := testsuites.Config{RunID: id, Namespace: get("NAMESPACE"), Token: token, RepoRoot: repoRoot}.Defaults()
	retain := get("E2E_RETAIN_ON_FAILURE")
	if retain != "" && retain != "0" && retain != "1" {
		return result, &Error{2, "E2E_RETAIN_ON_FAILURE must be 0 or 1"}
	}
	cfg.RetainOnFailure = retain == "1"
	for _, entry := range []struct {
		name  string
		value *time.Duration
	}{{"E2E_COMMAND_TIMEOUT", &cfg.CommandTimeout}, {"E2E_REQUEST_TIMEOUT", &cfg.RequestTimeout}, {"E2E_SCENARIO_TIMEOUT", &cfg.ScenarioTimeout}, {"E2E_TEARDOWN_TIMEOUT", &cfg.TeardownTimeout}, {"E2E_SHARED_IO_TIMEOUT", &cfg.SharedIOTimeout}} {
		if text := get(entry.name); text != "" {
			if strings.Trim(text, "0123456789") != "" {
				return result, &Error{2, entry.name + " must be a positive integer number of seconds"}
			}
			duration, err := time.ParseDuration(text + "s")
			if err != nil || duration <= 0 {
				return result, &Error{2, entry.name + " must be a positive integer number of seconds"}
			}
			*entry.value = duration
		}
	}
	for _, name := range result.Scenarios {
		if name == "shared-target" {
			cfg.LUNCount = 2
		}
	}
	if err := cfg.Validate(); err != nil {
		return result, &Error{2, err.Error()}
	}
	result.Harness = cfg
	result.Kubeconfig = get("KUBECONFIG")
	result.Images = images.Options{RepoRoot: repoRoot, Delivery: get("E2E_DRIVER_DELIVERY"), KindCluster: get("E2E_KIND_CLUSTER"), Arch: get("E2E_ARCH"), Timeout: cfg.CommandTimeout}
	if result.Images.Delivery == "" {
		result.Images.Delivery = "kind"
	}
	if result.Images.Arch == "" {
		result.Images.Arch = runtime.GOARCH
	}
	if result.Images.Arch != "amd64" && result.Images.Arch != "arm64" {
		return result, &Error{2, "E2E_ARCH must be amd64 or arm64"}
	}
	if result.Images.Delivery != "kind" && result.Images.Delivery != "registry" {
		return result, &Error{2, "E2E_DRIVER_DELIVERY must be kind or registry"}
	}
	if result.Images.Delivery == "kind" && result.Images.KindCluster == "" {
		return result, &Error{2, "set E2E_KIND_CLUSTER explicitly"}
	}
	result.DriverTag, result.TargetTag, result.ToolsTag = get("E2E_DRIVER_IMAGE"), get("E2E_TARGET_IMAGE"), get("E2E_TOOLS_IMAGE")
	if result.Images.Delivery == "kind" {
		if result.DriverTag == "" {
			result.DriverTag = "iscsi-driver:e2e-" + id
		}
		if result.TargetTag == "" {
			result.TargetTag = "iscsi-target:e2e-" + id
		}
		if result.ToolsTag == "" {
			result.ToolsTag = "iscsi-tools:e2e-" + id
		}
	}
	for _, image := range []string{result.DriverTag, result.TargetTag, result.ToolsTag} {
		if err := images.ValidateTag(image); err != nil {
			return result, &Error{2, fmt.Sprintf("invalid test image: %s", err)}
		}
	}
	return result, nil
}
