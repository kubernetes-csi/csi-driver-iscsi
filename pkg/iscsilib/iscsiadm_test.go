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
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDiscoverTargetCHAP(t *testing.T) {
	chap := Secrets{SecretsType: "chap", UserName: "user", Password: "pass"}
	tests := []struct {
		name            string
		chapDiscovery   bool
		chapSession     bool
		wantSessionAuth bool
	}{
		{name: "discovery CHAP only", chapDiscovery: true},
		{name: "session CHAP only", chapSession: true, wantSessionAuth: true},
		{name: "discovery and session CHAP", chapDiscovery: true, chapSession: true, wantSessionAuth: true},
	}

	defer func(orig func(string, []string, time.Duration) ([]byte, error)) { execWithTimeout = orig }(execWithTimeout)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls [][]string
			execWithTimeout = func(_ string, args []string, _ time.Duration) ([]byte, error) {
				calls = append(calls, args)
				return nil, nil
			}

			c := Connector{
				DoDiscovery:      true,
				DoCHAPDiscovery:  test.chapDiscovery,
				DoCHAPSession:    test.chapSession,
				DiscoverySecrets: chap,
				SessionSecrets:   chap,
			}
			if err := c.discoverTarget("iqn.2016-09.com.example:target", "default", "10.0.0.1:3260"); err != nil {
				t.Fatalf("discoverTarget() error = %v", err)
			}

			sessionAuth := false
			for _, args := range calls {
				cmd := strings.Join(args, " ")
				if slices.Contains(args, "node") && strings.Contains(cmd, "discovery.sendtargets.auth.") {
					t.Errorf("discovery CHAP set on node record: iscsiadm %s", cmd)
				}
				if slices.Contains(args, "node") && strings.Contains(cmd, "node.session.auth.username") {
					sessionAuth = true
				}
			}
			if sessionAuth != test.wantSessionAuth {
				t.Errorf("session CHAP set on node record = %v, want %v", sessionAuth, test.wantSessionAuth)
			}
		})
	}
}
