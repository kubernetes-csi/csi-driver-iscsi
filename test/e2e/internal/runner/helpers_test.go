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

package runner_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var repoRoot, _ = filepath.Abs("../../../..")
var commandFakes = map[string]func([]string) int{}

type commandResult struct {
	Code           int
	Stdout, Stderr string
}

func newFakeCommands(mode string, names ...string) (string, string) {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	binary, err := os.Executable()
	Expect(err).NotTo(HaveOccurred())
	for _, name := range names {
		Expect(os.Symlink(binary, filepath.Join(dir, name))).To(Succeed())
	}
	return dir, filepath.Join(dir, "commands")
}

func commandEnvironment(overrides map[string]string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	for key, value := range overrides {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func runCommand(env map[string]string, name string, args ...string) commandResult {
	GinkgoHelper()
	return runCommandTimeout(20*time.Second, env, name, args...)
}

func runCommandTimeout(timeout time.Duration, env map[string]string, name string, args ...string) commandResult {
	GinkgoHelper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = repoRoot
	cmd.Env = commandEnvironment(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	Expect(ctx.Err()).NotTo(HaveOccurred(), "test command timed out: %s %v\n%s", name, args, stderr.String())
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		Expect(err).NotTo(HaveOccurred())
	}
	return commandResult{Code: cmd.ProcessState.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String()}
}

func appendCommand(path string, call []string) error {
	data, err := json.Marshal(call)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	return errors.Join(err, f.Close())
}

func readCalls(path string) [][]string {
	GinkgoHelper()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = f.Close() }()
	var calls [][]string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var call []string
		Expect(json.Unmarshal(scanner.Bytes(), &call)).To(Succeed())
		calls = append(calls, call)
	}
	Expect(scanner.Err()).NotTo(HaveOccurred())
	return calls
}

func hasCall(calls [][]string, prefix ...string) bool {
	for _, call := range calls {
		if len(call) < len(prefix) {
			continue
		}
		match := true
		for i, part := range prefix {
			if call[i] != part {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
