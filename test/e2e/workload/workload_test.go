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

package workload_test

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/workload"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMain(m *testing.M) {
	if os.Getenv("IO_TEST_CHILD") == "1" {
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("IO_TEST_PID"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(90)
		}
		time.Sleep(20 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestWorkload(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Storage data workers") }

var _ = Describe("Storage payload operations", func() {
	It("seeds once with exact bytes and forces later reads to verify persistence", func() {
		path := filepath.Join(GinkgoT().TempDir(), "data")
		Expect(workload.Seed(path, "payload")).To(Succeed())
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(Equal([]byte("payload\n")))
		Expect(workload.Verify(path, "payload")).To(Succeed())
		Expect(workload.Seed(path, "replacement")).NotTo(Succeed())
		Expect(workload.Verify(path, "payload")).To(Succeed())
		Expect(workload.Verify(path, "replacement")).NotTo(Succeed())
	})
	It("detects missing and corrupt data without repairing it", func() {
		path := filepath.Join(GinkgoT().TempDir(), "data")
		Expect(workload.Verify(path, "payload")).NotTo(Succeed())
		Expect(os.WriteFile(path, []byte("payload"), 0600)).To(Succeed())
		Expect(workload.Verify(path, "payload")).To(MatchError("payload bytes differ"))
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("payload"))
	})
	It("writes back and rereads shared B data in each step", func() {
		dir := GinkgoT().TempDir()
		Expect(workload.Seed(filepath.Join(dir, "b-pattern"), workload.SharedPayload)).To(Succeed())
		Expect(workload.IOStep(dir)).To(Succeed())
		Expect(workload.Verify(filepath.Join(dir, "b-io"), workload.SharedPayload)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "b-pattern"), []byte("corrupt"), 0600)).To(Succeed())
		Expect(workload.IOStep(dir)).NotTo(Succeed())
		Expect(workload.Verify(filepath.Join(dir, "b-io"), workload.SharedPayload)).To(Succeed())
	})
	It("propagates write failures instead of publishing success", func() {
		dir := GinkgoT().TempDir()
		Expect(workload.Seed(filepath.Join(dir, "b-pattern"), workload.SharedPayload)).To(Succeed())
		Expect(os.Mkdir(filepath.Join(dir, "b-io"), 0700)).To(Succeed())
		Expect(workload.IOStep(dir)).NotTo(Succeed())
	})
})

var _ = Describe("Checked shared I/O supervision", func() {
	var dir, progress string
	BeforeEach(func() { dir = GinkgoT().TempDir(); progress = filepath.Join(dir, "progress") })
	inProcess := func(_ context.Context, mode, directory string) error {
		if mode == "io-init" {
			return workload.Seed(filepath.Join(directory, "b-pattern"), workload.SharedPayload)
		}
		return workload.IOStep(directory)
	}
	It("counts only completed checked steps and does not reseed after failure", func() {
		var steps int
		op := func(ctx context.Context, mode, directory string) error {
			if mode == "io-step" {
				steps++
				if steps == 3 {
					return errors.New("injected I/O failure")
				}
			}
			return inProcess(ctx, mode, directory)
		}
		err := workload.RunIO(context.Background(), dir, progress, time.Second, 0, op)
		Expect(err).To(MatchError(ContainSubstring("injected I/O failure")))
		data, readErr := os.ReadFile(progress)
		Expect(readErr).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("2\n"))
		Expect(workload.RunIO(context.Background(), dir, progress, time.Second, 0, inProcess)).NotTo(Succeed())
		data, readErr = os.ReadFile(progress)
		Expect(readErr).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("2\n"))
	})
	It("does not publish progress when initialization fails", func() {
		err := workload.RunIO(context.Background(), dir, progress, time.Second, 0, func(context.Context, string, string) error { return errors.New("seed failed") })
		Expect(err).To(MatchError(ContainSubstring("seed failed")))
		Expect(progress).NotTo(BeAnExistingFile())
	})
	It("creates the progress directory missing from a scratch image", func() {
		progress = filepath.Join(GinkgoT().TempDir(), "tmp", "worker", "io-progress")
		steps := 0
		op := func(ctx context.Context, mode, directory string) error {
			if mode == "io-step" {
				steps++
				if steps == 2 {
					return errors.New("stop after completed step")
				}
			}
			return inProcess(ctx, mode, directory)
		}
		err := workload.RunIO(context.Background(), dir, progress, time.Second, 0, op)
		Expect(err).To(MatchError(ContainSubstring("stop after completed step")))
		data, readErr := os.ReadFile(progress)
		Expect(readErr).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("1\n"))
	})
	It("fails before mounted I/O when the progress directory cannot be created", func() {
		parent := filepath.Join(GinkgoT().TempDir(), "file")
		Expect(os.WriteFile(parent, nil, 0600)).To(Succeed())
		calls := 0
		err := workload.RunIO(context.Background(), dir, filepath.Join(parent, "progress"), time.Second, 0, func(context.Context, string, string) error {
			calls++
			return nil
		})
		Expect(err).To(MatchError(ContainSubstring("create progress directory")))
		Expect(calls).To(BeZero())
	})
	DescribeTable("bounds initialization and later mounted I/O", func(hangMode string) {
		err := workload.RunIO(context.Background(), dir, progress, 20*time.Millisecond, 0, func(ctx context.Context, mode, directory string) error {
			if mode == hangMode {
				<-ctx.Done()
				return ctx.Err()
			}
			return inProcess(ctx, mode, directory)
		})
		Expect(err).To(MatchError(ContainSubstring("deadline exceeded")))
		Expect(progress).NotTo(BeAnExistingFile())
	}, Entry("initialization", "io-init"), Entry("checked copy", "io-step"))
	It("fails if an operation returns success after its deadline", func() {
		err := workload.RunIO(context.Background(), dir, progress, time.Millisecond, 0, func(ctx context.Context, _ string, _ string) error { <-ctx.Done(); return nil })
		Expect(err).To(MatchError(ContainSubstring("deadline exceeded")))
		Expect(progress).NotTo(BeAnExistingFile())
	})
	It("stops on progress publication errors", func() {
		Expect(os.Mkdir(progress, 0700)).To(Succeed())
		err := workload.RunIO(context.Background(), dir, progress, time.Second, 0, inProcess)
		Expect(err).To(MatchError(ContainSubstring("publish progress")))
	})
	It("honors cancellation without continuing checked operations", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var calls atomic.Int64
		err := workload.RunIO(ctx, dir, progress, time.Second, 0, func(ctx context.Context, _ string, _ string) error { calls.Add(1); return ctx.Err() })
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		Expect(calls.Load()).To(Equal(int64(1)))
		Expect(progress).NotTo(BeAnExistingFile())
	})
	It("does not start an operation after cancellation", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := workload.ProcessOperation("/this/binary/does/not/exist")(ctx, "io-step", dir)
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})
	It("kills and reaps a Go child process that cannot finish its operation", func() {
		pidFile := filepath.Join(dir, "child.pid")
		GinkgoT().Setenv("IO_TEST_CHILD", "1")
		GinkgoT().Setenv("IO_TEST_PID", pidFile)
		binary, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)
		done := make(chan error, 1)
		go func() { done <- workload.ProcessOperation(binary)(ctx, "io-step", dir) }()
		Eventually(pidFile, 2*time.Second, 10*time.Millisecond).Should(BeAnExistingFile())
		data, err := os.ReadFile(pidFile)
		Expect(err).NotTo(HaveOccurred())
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		cancel()
		Eventually(done, time.Second).Should(Receive(MatchError(context.Canceled)))
		Eventually(func() error { return syscall.Kill(pid, 0) }, time.Second, 10*time.Millisecond).Should(MatchError(syscall.ESRCH))
	})
})
