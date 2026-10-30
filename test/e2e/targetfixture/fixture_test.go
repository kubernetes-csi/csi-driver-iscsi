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

package targetfixture_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/targetfixture"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMain(m *testing.M) {
	if os.Getenv("TGTD_FAKE") == "1" {
		os.Exit(fakeCommand())
	}
	os.Exit(m.Run())
}
func TestTargetFixture(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Go target fixture") }

func fakeCommand() int {
	args := os.Args[1:]
	state := os.Getenv("TGTD_STATE")
	f, err := os.OpenFile(filepath.Join(state, "commands"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 99
	}
	data, _ := json.Marshal(append([]string{filepath.Base(os.Args[0])}, args...))
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
	if filepath.Base(os.Args[0]) == "fake-tgtd" {
		if os.Getenv("TGTD_DIE") == "1" {
			return 9
		}
		stopped := make(chan os.Signal, 1)
		if os.Getenv("IGNORE_TERM") == "1" {
			signal.Ignore(syscall.SIGTERM)
		} else {
			signal.Notify(stopped, syscall.SIGTERM)
		}
		_ = os.WriteFile(filepath.Join(state, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
		select {
		case <-stopped:
		case <-time.After(20 * time.Second):
		}
		return 0
	}
	value := func(key string) string {
		for i, s := range args {
			if s == key && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	mode, op := value("--mode"), value("--op")
	if (mode == "sys" && os.Getenv("HANG_CONTROL") == "1") || (mode == "target" && op == "show" && os.Getenv("HANG_PROBE") == "1") {
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(20 * time.Second)
	}
	if mode == "sys" {
		path := filepath.Join(state, "attempts")
		data, _ := os.ReadFile(path)
		n, _ := strconv.Atoi(string(data))
		n++
		_ = os.WriteFile(path, []byte(strconv.Itoa(n)), 0600)
		delay, _ := strconv.Atoi(os.Getenv("READY_DELAY"))
		if n <= delay {
			return 1
		}
		return 0
	}
	if mode == os.Getenv("FAIL_SETUP") && op == "new" {
		return 7
	}
	if mode == "target" && op == "show" {
		if value("--tid") != "" {
			fmt.Println("nop_interval=0")
			return 0
		}
		if os.Getenv("FAIL_PROBE") == "1" {
			return 8
		}
		issue := os.Getenv("PROBE_ISSUE")
		iqn, path := os.Getenv("IQN"), os.Getenv("DISK_PATH")
		if issue == "iqn" {
			iqn = "iqn.other"
		}
		if issue == "path" {
			path = "/other"
		}
		lun, online := "1", "Yes"
		if issue == "lun" {
			lun = "2"
		}
		if issue == "offline" {
			online = "No"
		}
		fmt.Printf("Target 1: %s\n    LUN: 0\n        Online: Yes\n        Backing store path: None\n", iqn)
		if issue == "other-target" {
			fmt.Println("Target 2: iqn.other")
		}
		fmt.Printf("    LUN: %s\n        Online: %s\n        Backing store path: %s\n", lun, online, path)
		if os.Getenv("LUN_COUNT") == "2" && issue != "missing-second" {
			online = "Yes"
			if issue == "second-offline" {
				online = "No"
			}
			fmt.Printf("    LUN: 2\n        Online: %s\n        Backing store path: %s\n", online, os.Getenv("DISK_PATH_B"))
		}
	}
	return 0
}

func setEnv(key, value string) {
	GinkgoHelper()
	old, exists := os.LookupEnv(key)
	Expect(os.Setenv(key, value)).To(Succeed())
	DeferCleanup(func() {
		if exists {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func fixture(extra map[string]string) targetfixture.Config {
	GinkgoHelper()
	root := GinkgoT().TempDir()
	binary, err := os.Executable()
	Expect(err).NotTo(HaveOccurred())
	for _, name := range []string{"fake-tgtd", "fake-tgtadm"} {
		Expect(os.Symlink(binary, filepath.Join(root, name))).To(Succeed())
	}
	values := map[string]string{"TGTD_FAKE": "1", "TGTD_STATE": root, "IQN": "iqn.2026-01.test:fixture", "DISK_PATH": filepath.Join(root, "data/disk0.img"), "DISK_PATH_B": filepath.Join(root, "data/disk1.img"), "LUN_COUNT": "1", "TGTD_DIE": "", "IGNORE_TERM": "", "HANG_CONTROL": "", "HANG_PROBE": "", "READY_DELAY": "", "FAIL_SETUP": "", "FAIL_PROBE": "", "PROBE_ISSUE": ""}
	// Race builds otherwise add a one-second sleep to every fake command's
	// exit, which would consume the deliberately short control deadline.
	values["GORACE"] = strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0")
	for k, v := range extra {
		values[k] = v
	}
	for k, v := range values {
		setEnv(k, v)
	}
	c := targetfixture.Config{IQN: values["IQN"], Paths: []string{values["DISK_PATH"]}, SizeBytes: 1024 * 1024, StartupAttempts: 4, StartupInterval: 0, ControlTimeout: 200 * time.Millisecond, ShutdownTimeout: 100 * time.Millisecond, Daemon: filepath.Join(root, "fake-tgtd"), Control: filepath.Join(root, "fake-tgtadm")}
	if values["LUN_COUNT"] == "2" {
		c.Paths = append(c.Paths, values["DISK_PATH_B"])
	}
	return c
}

type lockedOutput struct {
	buffer bytes.Buffer
	mu     sync.Mutex
	cancel context.CancelFunc
}

func (w *lockedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(p)
	if strings.Contains(string(p), "iSCSI target configured and ready") {
		if w.cancel != nil {
			w.cancel()
		}
	}
	return n, err
}
func (w *lockedOutput) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.buffer.String() }
func serve(c targetfixture.Config) (int, string, string) {
	GinkgoHelper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := &lockedOutput{cancel: cancel}
	diagnostics := &lockedOutput{}
	code := c.Serve(ctx, out, diagnostics)
	return code, out.String(), diagnostics.String()
}
func daemonStopped() {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("TGTD_STATE"), "pid"))
	if os.IsNotExist(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())
	pid, err := strconv.Atoi(string(data))
	Expect(err).NotTo(HaveOccurred())
	// Emergency cleanup is deliberately after the absence assertion.
	DeferCleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	Expect(syscall.Kill(pid, 0)).To(MatchError(syscall.ESRCH))
}

type closedDiagnostics struct{}

func (closedDiagnostics) Write([]byte) (int, error) { return 0, errors.New("diagnostic stream closed") }

var _ = Describe("Target supervisor", func() {
	DescribeTable("preserves failure status when diagnostics cannot be written", func(failure string, expected int) {
		extra := map[string]string{}
		if failure == "setup" {
			extra["FAIL_SETUP"] = "logicalunit"
		}
		if failure == "probe" {
			extra["FAIL_PROBE"] = "1"
		}
		c := fixture(extra)
		if failure == "configuration" {
			c.IQN = ""
		}
		Expect(c.Serve(context.Background(), &bytes.Buffer{}, closedDiagnostics{})).To(Equal(expected))
		daemonStopped()
	}, Entry("invalid configuration", "configuration", 2), Entry("LUN setup error", "setup", 7), Entry("readiness error", "probe", 8))
	It("retries control readiness and creates the requested backing size", func() {
		c := fixture(map[string]string{"READY_DELAY": "2"})
		code, out, diag := serve(c)
		Expect(code).To(Equal(143), diag)
		Expect(out).To(ContainSubstring("configured and ready"))
		attempts, err := os.ReadFile(filepath.Join(os.Getenv("TGTD_STATE"), "attempts"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(attempts)).To(Equal("3"))
		stat, err := os.Stat(c.Paths[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(stat.Size()).To(Equal(c.SizeBytes))
		daemonStopped()
	})
	DescribeTable("preserves startup failures without reporting readiness", func(extra map[string]string, want int) {
		c := fixture(extra)
		code, out, diag := serve(c)
		Expect(code).To(Equal(want), diag)
		Expect(out).NotTo(ContainSubstring("configured and ready"))
		daemonStopped()
	}, Entry("control never ready", map[string]string{"READY_DELAY": "99"}, 1), Entry("daemon exits", map[string]string{"TGTD_DIE": "1"}, 1), Entry("LUN setup fails", map[string]string{"FAIL_SETUP": "logicalunit"}, 7), Entry("probe fails", map[string]string{"FAIL_PROBE": "1"}, 8))
	It("bounds hanging control and kills and reaps an unresponsive daemon", func() {
		c := fixture(map[string]string{"HANG_CONTROL": "1", "IGNORE_TERM": "1"})
		c.StartupAttempts = 1
		start := time.Now()
		code, out, _ := serve(c)
		Expect(code).To(Equal(1))
		Expect(out).NotTo(ContainSubstring("configured and ready"))
		Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second))
		daemonStopped()
	})
	It("bounds readiness queries", func() {
		c := fixture(map[string]string{"HANG_PROBE": "1"})
		start := time.Now()
		Expect(c.Ready(context.Background())).To(MatchError(context.DeadlineExceeded))
		Expect(time.Since(start)).To(BeNumerically("<", time.Second))
	})
	It("kills and reaps a configured daemon which ignores TERM", func() {
		c := fixture(map[string]string{"IGNORE_TERM": "1"})
		code, out, diag := serve(c)
		Expect(code).To(Equal(143), diag)
		Expect(out).To(ContainSubstring("configured and ready"))
		daemonStopped()
	})
	It("preserves bytes in both LUNs across repeated starts", func() {
		c := fixture(map[string]string{"LUN_COUNT": "2"})
		before := map[string][]byte{}
		for _, path := range c.Paths {
			Expect(os.MkdirAll(filepath.Dir(path), 0755)).To(Succeed())
			data := make([]byte, c.SizeBytes)
			copy(data[4096:], path)
			Expect(os.WriteFile(path, data, 0600)).To(Succeed())
			before[path] = data
		}
		for range 2 {
			code, out, diag := serve(c)
			Expect(code).To(Equal(143), diag)
			Expect(out).To(ContainSubstring("configured and ready"))
			daemonStopped()
			for _, path := range c.Paths {
				data, err := os.ReadFile(path)
				Expect(err).NotTo(HaveOccurred())
				Expect(data).To(Equal(before[path]))
			}
		}
		for _, issue := range []string{"missing-second", "second-offline"} {
			setEnv("PROBE_ISSUE", issue)
			Expect(c.Ready(context.Background())).NotTo(Succeed())
		}
	})
	DescribeTable("refuses invalid existing backing without starting the daemon", func(issue string) {
		c := fixture(nil)
		path := c.Paths[0]
		Expect(os.MkdirAll(filepath.Dir(path), 0755)).To(Succeed())
		sentinel := filepath.Join(os.Getenv("TGTD_STATE"), "sentinel")
		Expect(os.WriteFile(sentinel, []byte("untouched"), 0600)).To(Succeed())
		switch issue {
		case "wrong-size":
			Expect(os.WriteFile(path, []byte("untouched"), 0600)).To(Succeed())
		case "symlink":
			Expect(os.Symlink(sentinel, path)).To(Succeed())
		case "directory":
			Expect(os.Mkdir(path, 0755)).To(Succeed())
		case "invalid-size":
			c.SizeBytes = 0
		}
		code, out, _ := serve(c)
		Expect(code).NotTo(BeZero())
		Expect(out).NotTo(ContainSubstring("configured and ready"))
		Expect(filepath.Join(os.Getenv("TGTD_STATE"), "pid")).NotTo(BeAnExistingFile())
		data, err := os.ReadFile(sentinel)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("untouched"))
		if issue == "wrong-size" {
			data, err = os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal("untouched"))
		}
	}, Entry("size mismatch", "wrong-size"), Entry("symlink", "symlink"), Entry("directory", "directory"), Entry("invalid size", "invalid-size"))
	DescribeTable("requires exact target and LUN inventory", func(issue string) {
		c := fixture(map[string]string{"PROBE_ISSUE": issue})
		err := c.Ready(context.Background())
		if issue == "" {
			Expect(err).NotTo(HaveOccurred())
		} else {
			Expect(err).To(HaveOccurred())
		}
	}, Entry("ready", ""), Entry("wrong IQN", "iqn"), Entry("wrong LUN", "lun"), Entry("offline LUN", "offline"), Entry("wrong path", "path"), Entry("LUN belongs to another target", "other-target"))
	It("requires validation of distinct paths and environment bounds", func() {
		_ = fixture(nil)
		for _, item := range [][2]string{{"DISK_SIZE", "0"}, {"TGTD_STARTUP_ATTEMPTS", "0"}, {"TGTD_CONTROL_TIMEOUT", "100"}, {"IQN", "iqn.invalid whitespace"}, {"LUN_COUNT", "3"}} {
			setEnv(item[0], item[1])
			_, err := targetfixture.FromEnv()
			Expect(err).To(HaveOccurred())
			setEnv(item[0], "")
		}
		setEnv("LUN_COUNT", "2")
		setEnv("DISK_PATH_B", os.Getenv("DISK_PATH"))
		_, err := targetfixture.FromEnv()
		Expect(err).To(HaveOccurred())
	})
})
