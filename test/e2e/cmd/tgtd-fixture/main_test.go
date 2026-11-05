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
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFixtureCLI(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Fixture restart guard") }

var _ = Describe("Restarting only the target container", func() {
	It("refuses a different PID1 executable without sending signals", func() {
		dir := GinkgoT().TempDir()
		self, init := filepath.Join(dir, "self"), filepath.Join(dir, "init")
		Expect(os.WriteFile(self, nil, 0600)).To(Succeed())
		Expect(os.WriteFile(init, nil, 0600)).To(Succeed())
		called := false
		Expect(requestRestart(self, init, func(int, syscall.Signal) error { called = true; return nil })).To(HaveOccurred())
		Expect(called).To(BeFalse())
	})
	It("sends TERM to PID1 when it is the same fixture executable", func() {
		dir := GinkgoT().TempDir()
		self, init := filepath.Join(dir, "self"), filepath.Join(dir, "init")
		Expect(os.WriteFile(self, nil, 0600)).To(Succeed())
		Expect(os.Symlink(self, init)).To(Succeed())
		called := false
		Expect(requestRestart(self, init, func(pid int, sig syscall.Signal) error {
			called = true
			Expect(pid).To(Equal(1))
			Expect(sig).To(Equal(syscall.SIGTERM))
			return nil
		})).To(Succeed())
		Expect(called).To(BeTrue())
	})
	It("propagates signal delivery failure", func() {
		self := filepath.Join(GinkgoT().TempDir(), "self")
		Expect(os.WriteFile(self, nil, 0600)).To(Succeed())
		failure := errors.New("signal failed")
		Expect(requestRestart(self, self, func(int, syscall.Signal) error { return failure })).To(MatchError(failure))
	})
})
