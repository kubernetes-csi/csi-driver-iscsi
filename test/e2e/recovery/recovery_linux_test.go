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

package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	iscsiLib "github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsilib"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/utils/mount"
)

func TestWaitForRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device")
	if err := waitForRemoval(context.Background(), []string{path}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForRemoval(ctx, []string{path}); !errors.Is(err, context.Canceled) {
		t.Fatalf("existing device was accepted: %v", err)
	}
}

func TestUnmountRejectsPathsOutsideFixture(t *testing.T) {
	for _, path := range []string{"/", "/mnt/a", filepath.Join(os.TempDir(), "unrelated", "a"), filepath.Join(os.TempDir(), "iscsi-recovery-test", "other")} {
		if err := Unmount(path); err == nil || !strings.Contains(err.Error(), "outside the recovery fixture") {
			t.Fatalf("Unmount(%q) = %v; expected path rejection before syscall", path, err)
		}
	}
}

func TestCompletedUnpublish(t *testing.T) {
	for _, code := range []codes.Code{codes.OK, codes.Internal, codes.FailedPrecondition, codes.DeadlineExceeded, codes.Canceled, codes.Unavailable, codes.Unknown} {
		t.Run(code.String(), func(t *testing.T) {
			rpcErr := status.Error(code, "test")
			err := completedUnpublish(rpcErr)
			if (err != nil) != (rpcErr != nil) || !errors.Is(err, rpcErr) {
				t.Fatalf("completedUnpublish(%s) = %v; must preserve RPC failure %v", code, err, rpcErr)
			}
		})
	}
}

func TestRecoveryMountPreconditions(t *testing.T) {
	survivor := mount.MountInfo{Major: 0, Minor: 42, MountPoint: "/b", FsType: "btrfs"}
	for _, tc := range []struct {
		name      string
		entries   []mount.MountInfo
		wantError bool
	}{
		{"anonymous surviving mount", []mount.MountInfo{survivor}, false},
		{"A remains mounted", []mount.MountInfo{survivor, {MountPoint: "/a"}}, true},
		{"B is absent", nil, true},
		{"unrelated filesystem", []mount.MountInfo{{Major: 0, Minor: 42, MountPoint: "/b", FsType: "tmpfs"}}, true},
		{"matching device masks regression", []mount.MountInfo{survivor, {Major: 8, Minor: 17, MountPoint: "/other"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateMounts(tc.entries, "/a", "/b", unix.Mkdev(8, 17))
			if (err != nil) != tc.wantError {
				t.Fatalf("validateMounts() = %v; wantError=%v", err, tc.wantError)
			}
		})
	}
}

func TestRecoveryConnectorPreconditions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*iscsiLib.Connector)
		wantError bool
	}{
		{"valid identity", func(*iscsiLib.Connector) {}, false},
		{"missing WWID", func(c *iscsiLib.Connector) { c.DeviceWWID = "" }, true},
		{"already detached", func(c *iscsiLib.Connector) { c.DevicesDetached = true }, true},
		{"different target", func(c *iscsiLib.Connector) { c.TargetIqn = "iqn.other" }, true},
		{"different LUN", func(c *iscsiLib.Connector) { c.Lun = 1 }, true},
		{"multiple paths", func(c *iscsiLib.Connector) { c.Devices = append(c.Devices, c.Devices[0]) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := iscsiLib.Device{Name: "sdb", Type: "disk", Transport: "iscsi"}
			c := iscsiLib.Connector{VolumeName: "iscsi-pv-b-test", TargetIqn: "iqn.test", TargetPortals: []string{"127.0.0.1:3260"}, Lun: 2, DeviceWWID: "naa.123", MountTargetDevice: &d, Devices: []iscsiLib.Device{d}}
			tc.change(&c)
			err := validateConnector(&c, "iscsi-pv-b-test", "127.0.0.1:3260", "iqn.test")
			if (err != nil) != tc.wantError {
				t.Fatalf("validateConnector() = %v; wantError=%v", err, tc.wantError)
			}
		})
	}
}
