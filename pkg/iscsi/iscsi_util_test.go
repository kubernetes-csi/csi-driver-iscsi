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

package iscsi

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	iscsiLib "github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsilib"
	"k8s.io/utils/mount"
)

func TestDeviceMounted(t *testing.T) {
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "/dev/sda", Path: "/other"}})
	for _, test := range []struct {
		device string
		want   bool
	}{{"/dev/sda", true}, {"/dev/sdb", false}} {
		got, err := deviceMounted(mounter, test.device)
		if err != nil || got != test.want {
			t.Fatalf("deviceMounted(%q) = %v, %v; want %v", test.device, got, err, test.want)
		}
	}
}

func TestUnsharedPortals(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "iscsi-a.json")
	connector := &iscsiLib.Connector{TargetIqn: "iqn.test", TargetPortals: []string{"one:3260", "two:3260"}}
	other := filepath.Join(dir, "iscsi-b.json")
	if err := os.WriteFile(other, []byte(`{"target_iqn":"iqn.test","target_portal":["two:3260"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	portals, shared, err := unsharedPortals(current, connector)
	if err != nil || !shared || !reflect.DeepEqual(portals, []string{"one:3260"}) {
		t.Fatalf("unsharedPortals = %v, %v, %v", portals, shared, err)
	}
	if err := os.WriteFile(other, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := unsharedPortals(current, connector); err == nil {
		t.Fatal("invalid connection record was accepted")
	}
}
