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
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestISCSICommandTimeouts(t *testing.T) {
	for _, config := range []struct {
		name          string
		timeouts      CommandTimeouts
		login, logout time.Duration
	}{
		{"defaults", CommandTimeouts{}, 150 * time.Second, 30 * time.Second},
		{"custom", CommandTimeouts{Login: 7 * time.Second, Logout: 11 * time.Second}, 7 * time.Second, 11 * time.Second},
		{"login only", CommandTimeouts{Login: time.Minute}, time.Minute, 30 * time.Second},
		{"logout only", CommandTimeouts{Logout: time.Minute}, 150 * time.Second, time.Minute},
	} {
		t.Run(config.name, func(t *testing.T) {
			var budgets []time.Duration
			mockISCSI(t, func(command string, args []string, timeout time.Duration) ([]byte, error) {
				if command != "iscsiadm" {
					t.Fatalf("unexpected command %s", command)
				}
				budgets = append(budgets, timeout)
				return nil, nil
			})
			if err := config.timeouts.LoginTarget("iqn.test", "192.0.2.1:3260"); err != nil {
				t.Fatal(err)
			}
			if err := config.timeouts.LogoutTarget("iqn.test", "192.0.2.1:3260"); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(budgets, []time.Duration{config.login, config.logout}) {
				t.Fatalf("budgets = %v", budgets)
			}
		})
	}
}

func TestNonLifecycleCommandTimeouts(t *testing.T) {
	for _, args := range [][]string{
		{"-m", "session"}, {"-m", "node", "-R"},
		{"-m", "discoverydb", "--discover"},
		{"-m", "iface", "-o", "show"}, {"-m", "node", "-o", "new"},
		{"-m", "node", "-o", "delete"},
		{"-m", "iface", "-I", "--login"},
		{"-m", "node", "-o", "update", "-n", "node.session.auth.password", "-v", "--logout"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			mockISCSI(t, func(_ string, got []string, timeout time.Duration) ([]byte, error) {
				if !reflect.DeepEqual(got, args) || timeout != 3*time.Second {
					t.Fatalf("args %v budget %v", got, timeout)
				}
				return nil, nil
			})
			if _, err := iscsiCmd(args...); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLifecycleCommandsOutliveShortBudget(t *testing.T) {
	// Model a four-second iscsid response without sleeping. The old fixed three
	// second budget fails this regression; both lifecycle defaults must succeed.
	mockISCSI(t, func(_ string, _ []string, timeout time.Duration) ([]byte, error) {
		if timeout < 4*time.Second {
			return nil, context.DeadlineExceeded
		}
		return nil, nil
	})
	for _, call := range []func(string, string) error{Login, Logout} {
		if err := call("iqn.test", "192.0.2.1:3260"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := GetSessions(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("query timeout expanded: %v", err)
	}
	for _, call := range []func(string, string) error{
		(CommandTimeouts{Login: time.Second}).LoginTarget,
		(CommandTimeouts{Logout: time.Second}).LogoutTarget,
	} {
		if err := call("iqn.test", "192.0.2.1:3260"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lost lifecycle timeout: %v", err)
		}
	}
}

func TestConnectorUsesLoginTimeout(t *testing.T) {
	skipWindows(t)
	noSessions := exitStatus(t, 21)
	called := false
	mockISCSI(t, func(_ string, args []string, timeout time.Duration) ([]byte, error) {
		if args[len(args)-1] == "-l" {
			called = true
			if timeout != 9*time.Second {
				t.Fatalf("login timeout = %v", timeout)
			}
			return nil, context.DeadlineExceeded
		}
		if timeout != 3*time.Second {
			t.Fatalf("query timeout = %v", timeout)
		}
		return nil, noSessions
	})
	c := Connector{CommandTimeouts: CommandTimeouts{Login: 9 * time.Second}}
	_, err := c.connectTarget("iqn.test", "192.0.2.1:3260", "default", "tcp")
	if !called || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("login: %v", err)
	}
}

func TestCommandTimeoutValidationAndPersistence(t *testing.T) {
	for _, timeouts := range []CommandTimeouts{{Login: -1}, {Logout: -1}} {
		mockISCSI(t, func(string, []string, time.Duration) ([]byte, error) {
			t.Fatal("invalid timeout executed a command")
			return nil, nil
		})
		if err := timeouts.LoginTarget("iqn.test", "192.0.2.1:3260"); err == nil {
			t.Fatal("negative timeout accepted")
		}
	}
	c := Connector{CommandTimeouts: CommandTimeouts{Login: time.Minute, Logout: time.Minute}}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var restored Connector
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.CommandTimeouts != (CommandTimeouts{}) {
		t.Fatal("runtime timeouts persisted")
	}
}

func TestConfiguredLogoutVerificationStaysShort(t *testing.T) {
	skipWindows(t)
	noSessions := exitStatus(t, 21)
	var budgets []time.Duration
	mockISCSI(t, func(_ string, _ []string, timeout time.Duration) ([]byte, error) {
		budgets = append(budgets, timeout)
		return nil, noSessions
	})
	if err := (CommandTimeouts{Logout: time.Minute}).LogoutTarget("iqn.test", "192.0.2.1:3260"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(budgets, []time.Duration{time.Minute, 3 * time.Second}) {
		t.Fatalf("logout/verification budgets = %v", budgets)
	}
}

func TestConnectorDisconnectCleanup(t *testing.T) {
	portals := []string{"192.0.2.1:3260", "192.0.2.2:3260", "192.0.2.3:3260"}
	for _, tc := range []struct {
		name       string
		failLogout int
	}{
		{"all logouts succeed", -1},
		{"first logout fails", 0},
		{"later logout fails", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wantCommands [][]string
			var wantBudgets []time.Duration
			for i, portal := range portals {
				wantCommands = append(wantCommands, []string{"-m", "node", "-T", "iqn.test", "-p", portal, "-u"})
				wantBudgets = append(wantBudgets, 17*time.Second)
				if i == tc.failLogout {
					break
				}
			}
			if tc.failLogout < 0 {
				wantCommands = append(wantCommands, []string{"-m", "node", "-T", "iqn.test", "-o", "delete"})
				wantBudgets = append(wantBudgets, 3*time.Second)
			}

			var commands [][]string
			var budgets []time.Duration
			mockISCSI(t, func(command string, args []string, timeout time.Duration) ([]byte, error) {
				if command != "iscsiadm" {
					t.Fatalf("unexpected command %s", command)
				}
				commands = append(commands, append([]string(nil), args...))
				budgets = append(budgets, timeout)
				if tc.failLogout >= 0 && args[len(args)-1] == "-u" && args[5] == portals[tc.failLogout] {
					return nil, context.DeadlineExceeded
				}
				return nil, nil
			})
			c := Connector{TargetIqn: "iqn.test", TargetPortals: portals, CommandTimeouts: CommandTimeouts{Logout: 17 * time.Second}}
			c.Disconnect()
			if !reflect.DeepEqual(commands, wantCommands) {
				t.Errorf("disconnect commands = %v, want %v", commands, wantCommands)
			}
			if !reflect.DeepEqual(budgets, wantBudgets) {
				t.Errorf("disconnect budgets = %v, want %v", budgets, wantBudgets)
			}
		})
	}
}
