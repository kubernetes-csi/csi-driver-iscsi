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

package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fixture tests the real Docker target without requiring an iSCSI initiator.
func Fixture(args []string) int {
	flags := flag.NewFlagSet("fixture-integration", flag.ContinueOnError)
	imageArg := flags.String("image", "", "tag to build from this checkout")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return 2
	}
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		fmt.Fprintln(os.Stderr, "Unable to create fixture identity")
		return 1
	}
	token := hex.EncodeToString(random)
	name := "iscsi-fixture-" + token
	volume := name + "-data"
	image := *imageArg
	if image == "" {
		image = name + ":test"
	}
	label := "iscsi.csi.k8s.io/fixture-test=" + token
	present := map[string]bool{"container": false, "volume": false}
	start := func(size string) error {
		_, err := fixtureDocker(60*time.Second, "run", "-d", "--name", name, "--label", label,
			"--mount", "type=volume,source="+volume+",target=/var/lib/iscsi", "-e", "DISK_SIZE="+size, image)
		if err == nil {
			present["container"] = true
		}
		return err
	}
	ready := func() error {
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := fixtureDocker(60*time.Second, "exec", name, "/tgtd-fixture", "ready"); err == nil {
				return nil
			}
			time.Sleep(time.Second)
		}
		return errors.New("target never became protocol-ready")
	}
	run := func() error {
		if _, err := fixtureDocker(600*time.Second, "build", "-f", filepath.Join(root, "deploy/example/tgtd/Dockerfile"), "-t", image, root); err != nil {
			return err
		}
		if _, err := fixtureDocker(60*time.Second, "volume", "create", "--label", label, volume); err != nil {
			return err
		}
		present["volume"] = true
		if err := start("8"); err != nil {
			return err
		}
		if err := ready(); err != nil {
			return err
		}
		if _, err := fixtureDocker(60*time.Second, "exec", name, "sh", "-c", "printf PR407-persistent-backing | dd of=/var/lib/iscsi/disk0.img bs=1 seek=4096 conv=notrunc status=none"); err != nil {
			return err
		}
		if _, err := fixtureDocker(60*time.Second, "restart", name); err != nil {
			return err
		}
		if err := ready(); err != nil {
			return err
		}
		actual, err := fixtureDocker(60*time.Second, "exec", name, "dd", "if=/var/lib/iscsi/disk0.img", "bs=1", "skip=4096", "count=24", "status=none")
		if err != nil {
			return err
		}
		if string(actual) != "PR407-persistent-backing" {
			return errors.New("backing bytes changed across container restart")
		}
		if _, err := fixtureDocker(60*time.Second, "rm", "-f", name); err != nil {
			return err
		}
		present["container"] = false
		if err := start("16"); err != nil {
			return err
		}
		deadline := time.Now().Add(20 * time.Second)
		state := ""
		for time.Now().Before(deadline) {
			output, err := fixtureDocker(60*time.Second, "inspect", "--format", "{{.State.Status}} {{.State.ExitCode}}", name)
			if err != nil {
				return err
			}
			state = strings.TrimSpace(string(output))
			if strings.HasPrefix(state, "exited ") {
				break
			}
			time.Sleep(time.Second)
		}
		if state != "exited 1" {
			return errors.New("incompatible backing size was not rejected")
		}
		return nil
	}
	result := 0
	if err := run(); err != nil {
		var dockerErr *fixtureCommandError
		if errors.As(err, &dockerErr) {
			result = dockerErr.code
			fmt.Fprintf(os.Stderr, "Fixture Docker command failed (exit %d)\n", result)
		} else {
			result = 1
			fmt.Fprintln(os.Stderr, "Fixture test failed:", err)
		}
	}
	for _, resource := range []struct{ kind, name string }{{"container", name}, {"volume", volume}} {
		format := "{{index .Config.Labels \"iscsi.csi.k8s.io/fixture-test\"}}"
		if resource.kind == "volume" {
			format = "{{index .Labels \"iscsi.csi.k8s.io/fixture-test\"}}"
		}
		found, err := fixtureDocker(60*time.Second, resource.kind, "inspect", "--format", format, resource.name)
		if err == nil && strings.TrimSpace(string(found)) == token {
			command := []string{"rm", "-f", resource.name}
			if resource.kind == "volume" {
				command = []string{"volume", "rm", resource.name}
			}
			if _, err := fixtureDocker(60*time.Second, command...); err != nil {
				fmt.Fprintln(os.Stderr, "Fixture cleanup failed for owned", resource.kind)
				if result == 0 {
					result = 1
				}
			}
		} else if present[resource.kind] || (err != nil && !isCommandFailure(err)) {
			fmt.Fprintln(os.Stderr, "Fixture cleanup could not verify owned", resource.kind)
			if result == 0 {
				result = 1
			}
		}
	}
	if result == 0 {
		fmt.Println("Fixture PASS: protocol readiness, backing bytes preserved, incompatible size rejected, cleanup complete")
	}
	return result
}

type fixtureCommandError struct{ code int }

func (e *fixtureCommandError) Error() string {
	return fmt.Sprintf("Docker command failed (exit %d)", e.code)
}
func isCommandFailure(err error) bool { var e *fixtureCommandError; return errors.As(err, &e) }
func fixtureDocker(timeout time.Duration, args ...string) ([]byte, error) {
	output, err := capture(timeout, "docker", args...)
	if err == nil {
		return output, nil
	}
	var commandErr interface{ ExitCode() int }
	if errors.As(err, &commandErr) {
		return nil, &fixtureCommandError{exitCode(err)}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, errors.New("docker command timed out")
	}
	return nil, errors.New("unable to run Docker command")
}
