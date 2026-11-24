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
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"k8s.io/klog/v2"
)

func TestExecWithTimeoutErrors(t *testing.T) {
	skipWindows(t)
	for _, tc := range []struct {
		name, script, want string
		code               int
		timeout            bool
	}{
		{name: "success keeps stdout separate", script: "printf result; printf diagnostic >&2", code: 0},
		{name: "exit status and stderr", script: "printf partial; printf 'session exists' >&2; exit 15", want: "session exists", code: 15},
		{name: "no stderr", script: "exit 21", code: 21},
		{name: "timeout keeps stderr", script: "printf 'logout interrupted' >&2; exec sleep 10", want: "logout interrupted", timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timeout := time.Second
			if tc.timeout {
				timeout = 100 * time.Millisecond
			}
			out, err := ExecWithTimeout("sh", []string{"-c", tc.script}, timeout)
			if tc.timeout {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lost timeout identity: %v", err)
				}
			} else if tc.code != 0 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.code {
					t.Fatalf("lost exit status %d: %v", tc.code, err)
				}
				if tc.want != "" && !strings.Contains(string(exit.Stderr), tc.want) {
					t.Fatalf("lost typed exit stderr: %q", exit.Stderr)
				}
			} else if err != nil || string(out) != "result" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("missing stderr %q: %v", tc.want, err)
			}
		})
	}
	_, err := ExecWithTimeout("/does-not-exist/iscsiadm", nil, time.Second)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost start error identity: %v", err)
	}
}

func TestExecWithTimeoutCHAPOutput(t *testing.T) {
	skipWindows(t)
	args := []string{"-c", "printf secret; printf secret >&2; exit 15", "-n", "node.session.auth.password", "-v", "secret"}
	out, err := ExecWithTimeout("sh", args, time.Second)
	var exit *exec.ExitError
	if len(out) != 0 || err == nil || strings.Contains(err.Error(), "secret") || !errors.As(err, &exit) || exit.ExitCode() != 15 || len(exit.Stderr) != 0 {
		t.Fatalf("CHAP output was exposed or status lost: %q, %v", out, err)
	}
}

func TestCHAPCommandDoesNotLogCredentials(t *testing.T) {
	skipWindows(t)
	flags := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(flags)
	if err := flags.Set("v", "2"); err != nil {
		t.Fatal(err)
	}
	if err := flags.Set("logtostderr", "false"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = flags.Set("v", "0"); _ = flags.Set("logtostderr", "true") }()
	var logs bytes.Buffer
	klog.SetOutput(&logs)
	defer klog.SetOutput(os.Stderr)
	original := execCommandContext
	execCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf private-chap-value; printf private-chap-value >&2; exit 15")
	}
	defer func() { execCommandContext = original }()
	for _, discovery := range []bool{false, true} {
		err := createCHAPEntries([]string{"-m", "node"}, Secrets{UserName: "private-chap-value", Password: "private-chap-value", UserNameIn: "private-chap-value", PasswordIn: "private-chap-value"}, discovery)
		if err == nil || strings.Contains(err.Error(), "private-chap-value") {
			t.Fatalf("unsafe CHAP error: %v", err)
		}
	}
	klog.Flush()
	if strings.Contains(logs.String(), "private-chap-value") {
		t.Fatalf("credentials in logs: %s", logs.String())
	}
}
