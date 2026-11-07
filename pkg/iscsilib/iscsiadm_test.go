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

func TestParseSessionsWithAndWithoutIQNSuffix(t *testing.T) {
	sessions := parseSessions("tcp: [1] 192.0.2.1:3260,1 iqn.2026-01.com.example\n" +
		"tcp: [2] 192.0.2.2:3261,1 iqn.2026-01.com.example:storage\n")
	if len(sessions) != 2 || sessions[0].IQN != "iqn.2026-01.com.example" || sessions[1].IQN != "iqn.2026-01.com.example:storage" {
		t.Fatalf("unexpected sessions: %+v", sessions)
	}
}

func TestDisconnectPreservesPortalPorts(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "commands")
	t.Setenv("ISCSI_TEST_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ISCSI_TEST_LOG\"\n"
	if err := os.WriteFile(filepath.Join(dir, "iscsiadm"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	Disconnect("iqn.test:target", []string{"192.0.2.1:3261", "192.0.2.1:3262"})
	output, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-p 192.0.2.1:3261 -u", "-p 192.0.2.1:3262 -u", "-o delete"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("missing %q in commands: %s", want, output)
		}
	}
}

func TestLogoutNoMatchingObjectsWithRemainingSession(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
if [ "$2" = session ]; then
  echo 'tcp: [1] 127.0.0.1:3260,1 iqn.test'
  exit 0
fi
exit 21
`
	if err := os.WriteFile(filepath.Join(dir, "iscsiadm"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Logout("iqn.test", "storage.example:3260"); err == nil {
		t.Fatal("logout accepted a remaining session for the target")
	}
	if err := Logout("iqn.test", "127.0.0.2:3260"); err != nil {
		t.Fatalf("another portal's session prevented logout: %v", err)
	}
}

func TestLogoutRejectsInvalidSessionListing(t *testing.T) {
	skipWindows(t)
	for _, output := range []string{"", "malformed"} {
		t.Run(output, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("ISCSI_TEST_SESSIONS", output)
			script := "#!/bin/sh\nif [ \"$2\" = session ]; then printf '%s' \"$ISCSI_TEST_SESSIONS\"; exit 0; fi\nexit 21\n"
			if err := os.WriteFile(filepath.Join(dir, "iscsiadm"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			if err := Logout("iqn.test", "127.0.0.1:3260"); err == nil {
				t.Fatal("logout accepted an invalid session listing")
			}
		})
	}
}

func TestDeviceWWID(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scsi_id"), []byte("#!/bin/sh\nprintf '%s' \"$SCSI_TEST_OUTPUT\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	device := Device{Name: "test", Type: "disk"}
	for _, tc := range []struct {
		name, output, want string
		wantError          bool
	}{
		{name: "with newline", output: "naa.123\n", want: "naa.123"},
		{name: "without newline", output: "naa.123", want: "naa.123"},
		{name: "empty", wantError: true},
		{name: "multiple identifiers", output: "naa.1\nnaa.2\n", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SCSI_TEST_OUTPUT", tc.output)
			got, err := device.WWID()
			if got != tc.want || (err != nil) != tc.wantError {
				t.Fatalf("WWID() = %q, %v; want %q, error %v", got, err, tc.want, tc.wantError)
			}
		})
	}
}
