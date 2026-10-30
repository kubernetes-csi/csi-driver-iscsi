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
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConfig(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Native e2e configuration") }

func lookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}

var _ = Describe("Scenario requests", func() {
	DescribeTable("reports known gates before running anything", func(selection, reason string) {
		_, err := Select(selection)
		var gate *Error
		Expect(errors.As(err, &gate)).To(BeTrue())
		Expect(gate.Code).To(Equal(78))
		Expect(gate.Message).To(ContainSubstring(reason))
		Expect(gate.Message).To(ContainSubstring("No scenarios ran"))
	}, Entry("CHAP", "chap-secret-ref", "nodePublishSecretRef"), Entry("stats", "stats", "NodeGetVolumeStats"), Entry("raw block", "raw-block", "raw block"), Entry("expansion", "expansion", "NodeExpandVolume"), Entry("multipath", "multipath", "distinct paths"), Entry("mixed", "baseline,stats", "NodeGetVolumeStats"), Entry("shared mixed", "shared-target,raw-block", "raw block"))
	DescribeTable("reports malformed requests as input errors", func(selection string) {
		_, err := Select(selection)
		var input *Error
		Expect(errors.As(err, &input)).To(BeTrue())
		Expect(input.Code).To(Equal(2))
		Expect(input.Message).NotTo(ContainSubstring("No scenarios ran"))
	}, Entry("empty", ""), Entry("trailing comma", "baseline,"), Entry("unknown", "made-up"), Entry("gate then unknown", "stats,made-up"), Entry("unknown then gate", "made-up,stats"), Entry("all mixed", "all,stats"), Entry("newline", "baseline\nstats"), Entry("CRLF", "baseline\r\nstats"), Entry("tab", "baseline\tmade-up"), Entry("space", "baseline stats"))
	It("keeps all limited to supported restart scenarios", func() {
		selected, err := Select("all")
		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(Equal([]string{"republish", "driver-restart", "target-restart"}))
	})
})

var _ = Describe("Environment validation", func() {
	base := func() map[string]string {
		return map[string]string{"E2E_RUN_ID": "unit", "E2E_KIND_CLUSTER": "scratch"}
	}
	It("defaults absent selection but rejects an explicitly empty selection", func() {
		values := base()
		cfg, err := FromEnvironment(lookup(values), "/checkout")
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Scenarios).To(Equal([]string{"baseline"}))
		Expect(cfg.Harness.Namespace).To(Equal("iscsi-e2e-unit"))
		Expect(cfg.DriverTag).To(Equal("iscsi-driver:e2e-unit"))
		Expect(cfg.Harness.Token).To(HaveLen(24))
		values["E2E_SCENARIOS"] = ""
		_, err = FromEnvironment(lookup(values), "/checkout")
		Expect(err).To(HaveOccurred())
	})
	It("uses two pre-existing LUNs only for a shared target request", func() {
		values := base()
		values["E2E_SCENARIOS"] = "shared-target"
		cfg, err := FromEnvironment(lookup(values), "/checkout")
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Harness.LUNCount).To(Equal(2))
	})
	DescribeTable("rejects invalid identities and budgets", func(key, value string) {
		values := base()
		values[key] = value
		_, err := FromEnvironment(lookup(values), "/checkout")
		var input *Error
		Expect(errors.As(err, &input)).To(BeTrue())
		Expect(input.Code).To(Equal(2))
	}, Entry("run ID", "E2E_RUN_ID", strings.Repeat("a", 21)), Entry("namespace", "NAMESPACE", "Uppercase"), Entry("zero timeout", "E2E_COMMAND_TIMEOUT", "0"), Entry("negative timeout", "E2E_TEARDOWN_TIMEOUT", "-1"), Entry("fractional timeout", "E2E_SHARED_IO_TIMEOUT", "1.5"), Entry("overflow", "E2E_SCENARIO_TIMEOUT", strings.Repeat("9", 30)), Entry("retention", "E2E_RETAIN_ON_FAILURE", "2"), Entry("arch", "E2E_ARCH", "windows"), Entry("delivery", "E2E_DRIVER_DELIVERY", "production"), Entry("kind must be explicit", "E2E_KIND_CLUSTER", ""))
	It("requires all registry images and preserves user timeouts", func() {
		values := base()
		values["E2E_DRIVER_DELIVERY"] = "registry"
		values["E2E_DRIVER_IMAGE"] = "127.0.0.1:5000/driver:test"
		values["E2E_TARGET_IMAGE"] = "127.0.0.1:5000/target:test"
		_, err := FromEnvironment(lookup(values), "/checkout")
		Expect(err).To(HaveOccurred())
		values["E2E_TOOLS_IMAGE"] = "127.0.0.1:5000/tools:test"
		values["E2E_COMMAND_TIMEOUT"] = "12"
		cfg, err := FromEnvironment(lookup(values), "/checkout")
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Harness.CommandTimeout).To(Equal(12 * time.Second))
	})
})

var _ = Describe("Real suite input gates", Ordered, func() {
	var binary, root string
	BeforeAll(func() {
		var err error
		root, err = filepath.Abs("../../..")
		Expect(err).NotTo(HaveOccurred())
		binary = filepath.Join(GinkgoT().TempDir(), "iscsi-e2e.test")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "test", "-mod=vendor", "-tags=e2e", "-c", "-o", binary, "./test/e2e")
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(output))
	})
	DescribeTable("exits before cluster or command execution", func(selection string, code int) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.v")
		cmd.Dir = root
		// With neither kubeconfig nor external commands available, a gate must
		// still return its own status rather than fail during setup.
		cmd.Env = []string{"E2E_SCENARIOS=" + selection, "PATH=/nonexistent", "KUBECONFIG=/nonexistent"}
		output, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		Expect(errors.As(err, &exit)).To(BeTrue())
		Expect(exit.ExitCode()).To(Equal(code), string(output))
		Expect(string(output)).NotTo(ContainSubstring("data check passed"))
		Expect(ctx.Err()).NotTo(HaveOccurred())
	}, Entry("known gate", "stats", 78), Entry("atomic gate", "baseline,stats", 78), Entry("unknown alongside gate", "stats,made-up", 2), Entry("empty selection", "", 2), Entry("hidden trailing gate", "baseline\nstats", 2))
	DescribeTable("rejects a non-executing suite result", func(argument string, code int, reason string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.v", argument)
		cmd.Dir = root
		cmd.Env = []string{"E2E_SCENARIOS=baseline", "E2E_KIND_CLUSTER=scratch", "PATH=/nonexistent", "KUBECONFIG=/nonexistent"}
		output, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		Expect(errors.As(err, &exit)).To(BeTrue())
		Expect(exit.ExitCode()).To(Equal(code), string(output))
		Expect(string(output)).To(ContainSubstring(reason))
		Expect(string(output)).NotTo(ContainSubstring("data check passed"))
		Expect(ctx.Err()).NotTo(HaveOccurred())
	}, Entry("empty focus", "-ginkgo.focus=NoSuchSpecCanMatch", 1, "Detected no specs ran"), Entry("dry run", "-ginkgo.dry-run", 2, "dry-run cannot establish an e2e result"))
})
