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
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func exitStatus(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if err == nil {
		t.Fatal("expected exit error")
	}
	return fmt.Errorf("iscsiadm: %w", err)
}

func mockISCSI(t *testing.T, f func(string, []string, time.Duration) ([]byte, error)) {
	t.Helper()
	original := execWithTimeout
	execWithTimeout = f
	t.Cleanup(func() { execWithTimeout = original })
}

func TestISCSIStageErrors(t *testing.T) {
	skipWindows(t)
	sentinel := exitStatus(t, 15)
	for _, tc := range []struct {
		name string
		call func() error
		want string
	}{
		{"discovery", func() error { return Discoverydb("192.0.2.1:3260", "default", Secrets{}, false) }, "discover targets"},
		{"login", func() error { return Login("iqn.test", "192.0.2.1:3260") }, "login to target iqn.test"},
		{"logout", func() error { return Logout("iqn.test", "192.0.2.1:3260") }, "logout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockISCSI(t, func(_ string, args []string, _ time.Duration) ([]byte, error) {
				if strings.Contains(strings.Join(args, " "), "-o new") {
					return nil, nil
				}
				return nil, sentinel
			})
			err := tc.call()
			if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wrong stage or lost cause: %v", err)
			}
		})
	}
}

func TestInterruptedLogoutThenLoginRetry(t *testing.T) {
	skipWindows(t)
	noSessions, alreadyExists := exitStatus(t, 21), exitStatus(t, 15)
	var calls []string
	mockISCSI(t, func(_ string, args []string, _ time.Duration) ([]byte, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		switch {
		case strings.HasSuffix(call, " -u"):
			return nil, fmt.Errorf("logout interrupted: %w", context.DeadlineExceeded)
		case call == "-m session":
			return nil, noSessions
		case strings.HasSuffix(call, " -l"):
			return nil, alreadyExists
		case strings.HasSuffix(call, " -R"):
			return nil, noSessions
		default:
			t.Fatalf("unexpected/destructive command: %s", call)
			return nil, nil
		}
	})
	if err := Logout("iqn.test", "192.0.2.1:3260"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost logout timeout: %v", err)
	}
	c := Connector{}
	_, err := c.connectTarget("iqn.test", "192.0.2.1:3260", "default", "tcp")
	if !errors.Is(err, alreadyExists) || !strings.Contains(err.Error(), "no matching kernel session") {
		t.Fatalf("accepted inconsistent daemon state: %v", err)
	}
	want := []string{"-m node -T iqn.test -p 192.0.2.1:3260 -u", "-m node -T iqn.test -p 192.0.2.1 -R", "-m session", "-m node -T iqn.test -p 192.0.2.1:3260 -l", "-m session"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands=%q, want %q", calls, want)
	}
}

type blockInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (i blockInfo) Mode() os.FileMode { return i.mode }

func TestExistingSessionRequiresUsableDevice(t *testing.T) {
	skipWindows(t)
	noSessions, alreadyExists := exitStatus(t, 21), exitStatus(t, 15)
	for _, tc := range []struct {
		name, listing, state       string
		mode                       os.FileMode
		missing, login, queryError bool
		success                    bool
	}{
		{name: "existing valid", listing: "tcp: [1] 192.0.2.1:3260,1 iqn.test", state: "running", mode: os.ModeDevice, success: true},
		{name: "login race valid", listing: "tcp: [1] 192.0.2.1:3260,1 iqn.test", state: "running", mode: os.ModeDevice, login: true, success: true},
		{name: "login race missing device", listing: "tcp: [1] 192.0.2.1:3260,1 iqn.test", missing: true, login: true},
		{name: "login race offline device", listing: "tcp: [1] 192.0.2.1:3260,1 iqn.test", state: "offline", mode: os.ModeDevice, login: true},
		{name: "existing regular file", listing: "tcp: [1] 192.0.2.1:3260,1 iqn.test"},
		{name: "existing character device", listing: "tcp: [1] 192.0.2.1:3260,1 iqn.test", mode: os.ModeDevice | os.ModeCharDevice},
		{name: "existing missing device", listing: "tcp: [1] 192.0.2.1:3260,1 iqn.test", missing: true},
		{name: "other target", listing: "tcp: [1] 192.0.2.1:3260,1 iqn.other", login: true},
		{name: "other port", listing: "tcp: [1] 192.0.2.1:3261,1 iqn.test", login: true},
		{name: "other portal", listing: "tcp: [1] 192.0.2.2:3260,1 iqn.test", login: true},
		{name: "unreadable sessions", queryError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stat, read, eval := osStat, osReadFile, evalSymlinks
			t.Cleanup(func() { osStat, osReadFile, evalSymlinks = stat, read, eval })
			osStat = func(string) (os.FileInfo, error) {
				if tc.missing {
					return nil, os.ErrNotExist
				}
				return blockInfo{mode: tc.mode}, nil
			}
			evalSymlinks = func(string) (string, error) { return "/dev/sda", nil }
			osReadFile = func(path string) ([]byte, error) {
				if path != "/sys/class/block/sda/device/state" {
					t.Fatalf("unexpected path %s", path)
				}
				return []byte(tc.state), nil
			}
			queries, logins := 0, 0
			mockISCSI(t, func(_ string, args []string, _ time.Duration) ([]byte, error) {
				call := strings.Join(args, " ")
				switch {
				case strings.HasSuffix(call, " -R"):
					return nil, nil
				case call == "-m session":
					queries++
					if tc.queryError {
						return nil, context.DeadlineExceeded
					}
					if tc.login && queries == 1 {
						return nil, noSessions
					}
					return []byte(tc.listing), nil
				case strings.HasSuffix(call, " -l"):
					logins++
					return nil, alreadyExists
				default:
					t.Fatalf("unexpected/destructive command %s", call)
					return nil, nil
				}
			})
			c := Connector{}
			path, err := c.connectTarget("iqn.test", "192.0.2.1:3260", "default", "tcp")
			if (err == nil) != tc.success {
				t.Fatalf("path=%s err=%v", path, err)
			}
			if tc.login && logins != 1 || !tc.login && logins != 0 {
				t.Fatalf("unexpected logins=%d", logins)
			}
			if tc.login && !tc.success && !errors.Is(err, alreadyExists) {
				t.Fatalf("lost login status: %v", err)
			}
			if tc.queryError && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost query error: %v", err)
			}
		})
	}
}

func TestRescanTimeoutDoesNotLogout(t *testing.T) {
	mockISCSI(t, func(_ string, args []string, _ time.Duration) ([]byte, error) {
		if !strings.HasSuffix(strings.Join(args, " "), " -R") {
			t.Fatalf("unexpected command: %v", args)
		}
		return nil, fmt.Errorf("rescan: %w", context.DeadlineExceeded)
	})
	c := Connector{}
	if _, err := c.connectTarget("iqn.test", "192.0.2.1:3260", "default", "tcp"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost timeout: %v", err)
	}
}

func TestEmptyOffloadDeviceGlob(t *testing.T) {
	original := filepathGlob
	defer func() { filepathGlob = original }()
	filepathGlob = func(string) ([]string, error) { return []string{}, nil }
	path := "/dev/disk/by-path/pci-*-ip-192.0.2.1:3260-iscsi-iqn.test-lun-0"
	if err := pathExists(&path, "iser"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected missing path error: %v", err)
	}
}

func TestInterruptedLogoutRetry(t *testing.T) {
	skipWindows(t)
	noSessions := exitStatus(t, 21)
	logouts := 0
	mockISCSI(t, func(_ string, args []string, _ time.Duration) ([]byte, error) {
		call := strings.Join(args, " ")
		if strings.HasSuffix(call, " -u") {
			logouts++
			if logouts == 1 {
				return nil, context.DeadlineExceeded
			}
			return nil, noSessions
		}
		if call == "-m session" {
			return nil, noSessions
		}
		t.Fatalf("unexpected command: %s", call)
		return nil, nil
	})
	if err := Logout("iqn.test", "192.0.2.1:3260"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost timeout: %v", err)
	}
	if err := Logout("iqn.test", "192.0.2.1:3260"); err != nil {
		t.Fatalf("retry with no kernel session failed: %v", err)
	}
}

func TestConnectPreservesLoginErrorAndSharedInterface(t *testing.T) {
	skipWindows(t)
	noSessions, alreadyExists := exitStatus(t, 21), exitStatus(t, 15)
	mockISCSI(t, func(_ string, args []string, _ time.Duration) ([]byte, error) {
		call := strings.Join(args, " ")
		switch {
		case call == "-m iface -o show -I default":
			return []byte("iface.transport_name = tcp\n"), nil
		case call == "-m session":
			return nil, noSessions
		case strings.HasSuffix(call, " -R"):
			return nil, noSessions
		case strings.HasSuffix(call, " -l"):
			return nil, alreadyExists
		default:
			t.Fatalf("unexpected/destructive command: %s", call)
			return nil, nil
		}
	})
	c := Connector{TargetIqn: "iqn.test", TargetPortals: []string{"192.0.2.1:3260"}}
	if _, err := c.Connect(); !errors.Is(err, alreadyExists) {
		t.Fatalf("lost login error through Connect: %v", err)
	}
}
