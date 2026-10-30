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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func init() { commandFakes["fixture-runner"] = fixtureRunnerFake }

func fixtureRunnerFake(args []string) int {
	state := map[string]string{}
	if data, err := os.ReadFile(os.Getenv("STATE")); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if err := appendCommand(os.Getenv("COMMAND_LOG"), args); err != nil {
		return 90
	}
	failure := os.Getenv("FAIL")
	if args[0] == "build" && failure == "build" {
		return 19
	}
	value := func(flag string) string {
		for i, arg := range args {
			if arg == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	if len(args) > 1 && args[0] == "volume" && args[1] == "create" {
		state["volume"] = args[len(args)-1]
		_, state["token"], _ = strings.Cut(value("--label"), "=")
	}
	if args[0] == "run" {
		state["container"], state["size"] = value("--name"), value("-e")
	}
	if args[0] == "exec" {
		for _, arg := range args {
			if strings.HasPrefix(arg, "count=") {
				if failure == "data" {
					fmt.Print("wrong")
				} else {
					fmt.Print("PR407-persistent-backing")
				}
				break
			}
		}
	}
	if args[0] == "inspect" {
		fmt.Println("exited 1")
	}
	if len(args) > 1 && (args[0] == "container" || args[0] == "volume") && args[1] == "inspect" {
		if _, ok := state[args[0]]; !ok {
			return 1
		}
		if failure == "foreign" && args[0] == "container" {
			fmt.Println("foreign-token")
		} else {
			fmt.Println(state["token"])
		}
	}
	if args[0] == "rm" {
		delete(state, "container")
	}
	if len(args) > 1 && args[0] == "volume" && args[1] == "rm" {
		if failure == "cleanup" {
			return 21
		}
		delete(state, "volume")
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(os.Getenv("STATE"), data, 0600); err != nil {
		return 91
	}
	return 0
}

var _ = Describe("Docker fixture runner", Ordered, func() {
	var binary string
	BeforeAll(func() {
		dir, err := os.MkdirTemp("", "iscsi-fixture-runner-tests-")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, dir)
		binary = filepath.Join(dir, "fixture-integration")
		result := runCommandTimeout(2*time.Minute, nil, "go", "-C", repoRoot, "build", "-buildvcs=false", "-mod=readonly", "-o", binary, "./test/e2e/cmd/fixture-integration")
		Expect(result.Code).To(BeZero(), result.Stderr)
	})
	runFixture := func(failure string) (commandResult, [][]string, map[string]string) {
		dir, log := newFakeCommands("fixture-runner", "docker")
		stateFile := filepath.Join(dir, "state")
		result := runCommand(map[string]string{"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"), "E2E_GO_FAKE": "fixture-runner", "STATE": stateFile, "COMMAND_LOG": log, "FAIL": failure}, binary)
		state := map[string]string{}
		if data, err := os.ReadFile(stateFile); err == nil {
			Expect(json.Unmarshal(data, &state)).To(Succeed())
		}
		return result, readCalls(log), state
	}
	It("builds the current source, checks restart persistence, and cleans owned resources", func() {
		result, calls, state := runFixture("")
		Expect(result.Code).To(BeZero(), result.Stderr)
		built, starts := false, 0
		for _, call := range calls {
			if call[0] == "build" && call[len(call)-1] == repoRoot {
				built = true
			}
			if call[0] == "run" {
				starts++
			}
		}
		Expect(built).To(BeTrue())
		Expect(hasCall(calls, "restart")).To(BeTrue())
		Expect(starts).To(Equal(2))
		Expect(state).NotTo(HaveKey("container"))
		Expect(state).NotTo(HaveKey("volume"))
		Expect(result.Stdout).To(ContainSubstring("Fixture PASS"))
	})
	It("fails on data corruption and cleans its resources", func() {
		result, _, state := runFixture("data")
		Expect(result.Code).NotTo(BeZero())
		Expect(result.Stdout).NotTo(ContainSubstring("Fixture PASS"))
		Expect(state).NotTo(HaveKey("container"))
		Expect(state).NotTo(HaveKey("volume"))
	})
	It("preserves a build failure without creating resources", func() {
		result, calls, _ := runFixture("build")
		Expect(result.Code).To(Equal(19))
		Expect(hasCall(calls, "volume", "create")).To(BeFalse())
		Expect(hasCall(calls, "run")).To(BeFalse())
	})
	It("fails when cleanup fails", func() {
		result, _, _ := runFixture("cleanup")
		Expect(result.Code).NotTo(BeZero())
		Expect(result.Stdout).NotTo(ContainSubstring("Fixture PASS"))
	})
	It("refuses to clean a container with a foreign identity", func() {
		result, _, state := runFixture("foreign")
		Expect(result.Code).NotTo(BeZero())
		Expect(state).To(HaveKey("container"))
		Expect(result.Stdout).NotTo(ContainSubstring("Fixture PASS"))
	})
})
