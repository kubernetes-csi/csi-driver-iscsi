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

package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTools(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Go probe and workload CLI") }

type closedWriter struct{}

func (closedWriter) Write([]byte) (int, error) { return 0, errors.New("output stream closed") }

var _ = Describe("Go tools command interface", func() {
	It("preserves input failure when diagnostics cannot be written", func() {
		Expect(run(nil, closedWriter{}, closedWriter{})).To(Equal(2))
		Expect(run([]string{"unknown"}, closedWriter{}, closedWriter{})).To(Equal(2))
	})
	It("fails when captured device identities cannot be written", func() {
		root := GinkgoT().TempDir()
		for _, dir := range []string{"proc/1", "sys/class/block/sdb", "sys/class/iscsi_session/session1/device/target1:0:0/1:0:0:1/block/sdb", "dev/disk/by-path"} {
			Expect(os.MkdirAll(filepath.Join(root, dir), 0755)).To(Succeed())
		}
		Expect(os.WriteFile(filepath.Join(root, "proc/1/mountinfo"), nil, 0600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "sys/class/iscsi_session/session1/targetname"), []byte("iqn.test:owned\n"), 0600)).To(Succeed())
		var diagnostics bytes.Buffer
		Expect(run([]string{"snapshot", "--root=" + root, "--iqn=iqn.test:owned", "--volume=pv-test"}, closedWriter{}, &diagnostics)).To(Equal(1))
		Expect(diagnostics.String()).To(ContainSubstring("output stream closed"))
	})
	execute := func(args ...string) (int, string, string) {
		var out, err bytes.Buffer
		code := run(args, &out, &err)
		return code, out.String(), err.String()
	}
	DescribeTable("rejects malformed input", func(args []string) {
		code, _, _ := execute(args...)
		Expect(code).To(Equal(2))
	}, Entry("missing subcommand", []string{}), Entry("unknown subcommand", []string{"unknown"}), Entry("unknown flag", []string{"verify", "--bad"}), Entry("extra positional argument", []string{"verify", "extra"}), Entry("missing payload path", []string{"verify"}), Entry("missing connect address", []string{"connect"}), Entry("nonpositive connect deadline", []string{"connect", "--address=127.0.0.1:1", "--timeout=0s"}))
	It("seeds and verifies bytes without reseeding", func() {
		path := filepath.Join(GinkgoT().TempDir(), "data")
		code, out, stderr := execute("seed", "--path="+path, "--payload=known payload")
		Expect(code).To(Equal(0), stderr)
		Expect(out).To(BeEmpty())
		code, _, stderr = execute("verify", "--path="+path, "--payload=known payload")
		Expect(code).To(Equal(0), stderr)
		code, _, _ = execute("seed", "--path="+path, "--payload=new")
		Expect(code).To(Equal(1))
		code, out, stderr = execute("read", "--path="+path)
		Expect(code).To(Equal(0), stderr)
		Expect(out).To(Equal("known payload\n"))
		code, _, _ = execute("verify", "--path="+path, "--payload=new")
		Expect(code).To(Equal(1))
	})
	It("exposes only numeric shared-I/O progress", func() {
		path := filepath.Join(GinkgoT().TempDir(), "progress")
		Expect(os.WriteFile(path, []byte("42\n"), 0600)).To(Succeed())
		code, out, stderr := execute("progress", "--path="+path)
		Expect(code).To(Equal(0), stderr)
		Expect(out).To(Equal("42\n"))
		Expect(os.WriteFile(path, []byte("failed\n"), 0600)).To(Succeed())
		code, out, _ = execute("progress", "--path="+path)
		Expect(code).To(Equal(1))
		Expect(out).To(BeEmpty())
	})
	It("distinguishes input and observation errors from residual resources", func() {
		root := GinkgoT().TempDir()
		code, _, stderr := execute("check", "--root="+root, "--iqn=iqn.test:owned", "--volume=pv-test")
		Expect(code).To(Equal(2))
		Expect(stderr).To(ContainSubstring("Cannot observe"))
		for _, dir := range []string{"proc/1", "sys/class/block", "sys/class/iscsi_session/session1", "dev/disk/by-path"} {
			Expect(os.MkdirAll(filepath.Join(root, dir), 0755)).To(Succeed())
		}
		Expect(os.WriteFile(filepath.Join(root, "proc/1/mountinfo"), nil, 0600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "sys/class/iscsi_session/session1/targetname"), []byte("iqn.test:owned\n"), 0600)).To(Succeed())
		code, _, stderr = execute("check", "--root="+root, "--iqn=iqn.test:owned", "--volume=pv-test")
		Expect(code).To(Equal(1))
		Expect(stderr).To(ContainSubstring("session remains"))
	})
	It("checks target TCP connectivity without an external command", func() {
		original := dialTarget
		DeferCleanup(func() { dialTarget = original })
		connection, peer := net.Pipe()
		DeferCleanup(func() { _ = peer.Close() })
		dialTarget = func(_ context.Context, budget time.Duration, address string) (net.Conn, error) {
			Expect(address).To(Equal("127.0.0.1:3260"))
			Expect(budget).To(Equal(time.Second))
			return connection, nil
		}
		code, _, stderr := execute("connect", "--address=127.0.0.1:3260", "--timeout=1s")
		Expect(code).To(Equal(0), stderr)
		_, err := peer.Write([]byte("closed"))
		Expect(err).To(HaveOccurred())
	})
	It("installs the helper and accepts an identical executable on retry", func() {
		dir := GinkgoT().TempDir()
		source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
		Expect(os.WriteFile(source, []byte("helper"), 0600)).To(Succeed())
		Expect(install(source, target)).To(Succeed())
		data, err := os.ReadFile(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("helper"))
		info, err := os.Stat(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm() & 0111).NotTo(BeZero())
		Expect(install(source, target)).To(Succeed())
		after, err := os.Stat(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.SameFile(info, after)).To(BeTrue())
	})
	It("requires recovery identity and a positive deadline", func() {
		for _, args := range [][]string{
			{"install"},
			{"btrfs-recovery"},
			{"removal-recovery"},
			{"removal-recovery", "--address=127.0.0.1:3260", "--iqn=iqn.test", "--volume=test", "--timeout=0s"},
			{"btrfs-recovery", "--address=127.0.0.1:3260", "--iqn=iqn.test", "--volume=test", "--timeout=0s"},
		} {
			code, _, _ := execute(args...)
			Expect(code).To(Equal(2))
		}
	})
})

func TestInstallRejectsExistingTargets(t *testing.T) {
	for _, kind := range []string{"different", "partial", "non-executable", "symlink", "dangling-symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
			if err := os.WriteFile(source, []byte("helper"), 0755); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "different":
				err = os.WriteFile(target, []byte("another helper"), 0755)
			case "partial":
				err = os.WriteFile(target, []byte("hel"), 0755)
			case "non-executable":
				err = os.WriteFile(target, []byte("helper"), 0644)
			case "symlink":
				err = os.Symlink(source, target)
			case "dangling-symlink":
				err = os.Symlink(filepath.Join(dir, "missing"), target)
			case "directory":
				err = os.Mkdir(target, 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := install(source, target); err == nil {
				t.Fatal("accepted an unsafe existing target")
			}
			after, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
				t.Fatal("existing target was modified")
			}
			data, err := os.ReadFile(source)
			if err != nil || string(data) != "helper" {
				t.Fatalf("source modified: %q, %v", data, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 {
				t.Fatalf("temporary installation files leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestInstallCopyFailureCanRetry(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	// Reading a directory fails after opening it, exercising the copy error path.
	if err := install(dir, target); err == nil {
		t.Fatal("directory source unexpectedly installed")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("failed copy published a target: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed copy leaked temporary files: %v, %v", entries, err)
	}
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("helper"), 0600); err != nil {
		t.Fatal(err)
	}
	// A killed installer can leave its private staging file, never the target.
	if err := os.WriteFile(filepath.Join(dir, ".e2e-tools-interrupted"), []byte("hel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := install(source, target); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}

func TestInstallConcurrentRetries(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if err := os.WriteFile(source, []byte("helper"), 0600); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for range 8 {
		go func() { results <- install(source, target) }()
	}
	for range 8 {
		if err := <-results; err != nil {
			t.Errorf("concurrent install: %v", err)
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "helper" {
		t.Fatalf("installed bytes = %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary installation files leaked: %v, %v", entries, err)
	}
}
