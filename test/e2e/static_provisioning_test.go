//go:build e2e

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

package e2e

import (
	"context"
	"fmt"
	"time"

	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/images"
	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/testsuites"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Completion is checked separately from Ginkgo's selection so a focus/skip
// expression cannot silently turn an explicit scenario request into a pass.
var completed = map[string]bool{}
var cleanupSuite func() error

var _ = ginkgo.AfterSuite(func() {
	// Ordered DeferCleanup can attach to a skipped final spec. Ginkgo v2.21
	// can then report its failure as additional information but return success.
	// A suite-level failure has its own outcome, independent of skipped specs.
	if cleanupSuite != nil {
		gomega.Expect(cleanupSuite()).To(gomega.Succeed(), "node teardown must pass before supporting resources are removed")
	}
	gomega.Expect(completed["baseline"]).To(gomega.BeTrue(), "baseline data verification did not run")
	for _, scenario := range settings.Scenarios {
		gomega.Expect(completed[scenario]).To(gomega.BeTrue(), "requested scenario %s did not run", scenario)
	}
})

var _ = ginkgo.Describe("Statically provisioned iSCSI volumes", ginkgo.Ordered, ginkgo.Serial, func() {
	var harness *testsuites.Harness
	var hadFailure bool
	ginkgo.ReportAfterEach(func(report ginkgo.SpecReport) { hadFailure = hadFailure || report.Failed() })
	ginkgo.BeforeAll(func(ctx ginkgo.SpecContext) {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		if settings.Kubeconfig != "" {
			rules.ExplicitPath = settings.Kubeconfig
		}
		loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
		raw, err := loader.RawConfig()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		restConfig, err := loader.ClientConfig()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		restConfig.Timeout = settings.Harness.RequestTimeout
		client, err := kubernetes.NewForConfig(restConfig)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// Read-only preflight happens before any build, image delivery or create.
		preflight, cancel := context.WithTimeout(ctx, settings.Harness.CommandTimeout)
		err = testsuites.Preflight(preflight, client, settings.Harness)
		cancel()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		options := settings.Images
		options.CurrentContext = raw.CurrentContext
		manager := &images.Manager{Options: options, Runner: images.ProcessRunner{}}
		selected, err := manager.Prepare(ctx, settings.DriverTag, settings.TargetTag, settings.ToolsTag)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cfg := settings.Harness
		cfg.DriverImage, cfg.TargetImage, cfg.ToolsImage = selected.Driver.Image, selected.Target.Image, selected.Tools.Image
		cfg.DriverPullPolicy, cfg.TargetPullPolicy, cfg.ToolsPullPolicy = selected.Driver.Policy, selected.Target.Policy, selected.Tools.Policy
		harness = testsuites.New(client, testsuites.NewPodExecutor(restConfig, client), cfg)
		harness.VerifyDriver = func(ctx context.Context, pods []corev1.Pod) error {
			return manager.Verify(ctx, pods, "iscsi", selected.Driver)
		}
		harness.VerifyTarget = func(ctx context.Context, pod *corev1.Pod) error {
			return manager.Verify(ctx, []corev1.Pod{*pod}, "tgtd", selected.Target)
		}
		harness.VerifyTools = func(ctx context.Context, pods []corev1.Pod) error {
			return manager.Verify(ctx, pods, "prerequisites", selected.Tools)
		}
		cleanupSuite = func() error {
			if cfg.RetainOnFailure && (hadFailure || ginkgo.CurrentSpecReport().Failed()) {
				fmt.Println("Retaining failed invocation", cfg.Token, "namespace", cfg.Namespace)
				return fmt.Errorf("retained failed invocation; node teardown was not verified")
			}
			cleanup, cancel := context.WithTimeout(context.Background(), cfg.TeardownTimeout)
			defer cancel()
			return harness.Cleanup(cleanup)
		}
		gomega.Expect(harness.Prepare(ctx)).To(gomega.Succeed())
		fmt.Printf("\nDriver image: %s; local ID: %s\n", selected.Driver.Image, selected.Driver.LocalID)
		fmt.Printf("Target image: %s; local ID: %s\n", selected.Target.Image, selected.Target.LocalID)
	}, ginkgo.NodeTimeout(15*time.Minute))
	ginkgo.It("mounts a pre-existing LUN and preserves seeded data", func(ctx ginkgo.SpecContext) {
		gomega.Expect(harness.Baseline(ctx)).To(gomega.Succeed())
		completed["baseline"] = true
		fmt.Println("\nScenario baseline data check passed; overall teardown pending")
	}, ginkgo.SpecTimeout(10*time.Minute))
	for _, scenario := range []string{"republish", "driver-restart", "target-restart", "shared-target"} {
		ginkgo.It("preserves data across "+scenario, func(ctx ginkgo.SpecContext) {
			selected := false
			for _, name := range settings.Scenarios {
				if name == scenario {
					selected = true
				}
			}
			if !selected {
				ginkgo.Skip("scenario not requested")
			}
			gomega.Expect(harness.RunScenario(ctx, scenario)).To(gomega.Succeed())
			completed[scenario] = true
			fmt.Printf("\nScenario %s data check passed; overall teardown pending\n", scenario)
		}, ginkgo.SpecTimeout(10*time.Minute))
	}
})
