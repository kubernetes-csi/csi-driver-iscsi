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
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/config"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var settings config.Runtime
var kubeconfig = flag.String("kubeconfig", os.Getenv("KUBECONFIG"), "Disposable cluster kubeconfig")

func TestMain(m *testing.M) {
	flag.Parse()
	suiteConfig, _ := ginkgo.GinkgoConfiguration()
	if suiteConfig.DryRun {
		fmt.Fprintln(os.Stderr, "dry-run cannot establish an e2e result")
		os.Exit(2)
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot determine checkout directory")
		os.Exit(2)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "deploy/csi-iscsi-node.yaml")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			fmt.Fprintln(os.Stderr, "run the suite from the driver checkout")
			os.Exit(2)
		}
		root = parent
	}
	settings, err = config.FromEnvironment(os.LookupEnv, root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		var input *config.Error
		if errors.As(err, &input) {
			os.Exit(input.Code)
		}
		os.Exit(2)
	}
	if *kubeconfig != "" {
		settings.Kubeconfig = *kubeconfig
	}
	os.Exit(m.Run())
}

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	suiteConfig.FailOnEmpty = true
	ginkgo.RunSpecs(t, "iSCSI static volume end-to-end tests", suiteConfig, reporterConfig)
}

func TestDiagnostics(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "commands")
	t.Setenv("DIAGNOSTIC_TEST_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	commands := map[string]string{
		"kubectl": "printf 'kubectl %s\\n' \"$*\" >> \"$DIAGNOSTIC_TEST_LOG\"\nexit 1\n",
		"kind":    "printf 'kind %s\\n' \"$*\" >> \"$DIAGNOSTIC_TEST_LOG\"\nprintf 'kind-control-plane\\n'\n",
		"docker":  "printf 'docker %s\\n' \"$*\" >> \"$DIAGNOSTIC_TEST_LOG\"\n",
	}
	for name, body := range commands {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	collectDiagnostics("test-ns", "kind-test", "/tmp/test-kubeconfig")
	output, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--context kind-test", "--kubeconfig /tmp/test-kubeconfig", "-n test-ns describe pods", "get events", "-n kube-system logs", "kind get nodes --name test", "docker exec kind-control-plane journalctl -u kubelet"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("missing diagnostic command %q in %s", want, output)
		}
	}
}
