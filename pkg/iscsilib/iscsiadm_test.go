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

package iscsilib

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test requires Unix executable scripts")
	}
}

func TestIscsiadmWrapperTimeout(t *testing.T) {
	skipWindows(t)
	manifest, err := os.ReadFile("../../deploy/csi-iscsi-node.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, script, ok := strings.Cut(string(manifest), "  iscsiadm: |\n")
	if !ok {
		t.Fatal("iscsiadm wrapper not found")
	}
	script, _, _ = strings.Cut(script, "\n---")
	script = strings.TrimPrefix(strings.ReplaceAll(script, "\n    ", "\n"), "    ")
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "iscsiadm")
	if err := os.WriteFile(wrapper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chroot"), []byte("#!/bin/sh\nexec sleep 4\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := time.Now()
	_, err = ExecWithTimeout(wrapper, nil, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("wrapper timeout took %v and returned %v", time.Since(start), err)
	}
}
