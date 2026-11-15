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
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
	"k8s.io/utils/mount"
)

type testDeviceInfo struct {
	os.FileInfo
	rdev uint64
}

func (info testDeviceInfo) Sys() any { return &syscall.Stat_t{Rdev: info.rdev} }

func TestDeviceInMountInfo(t *testing.T) {
	info := testDeviceInfo{rdev: unix.Mkdev(8, 17)}
	mounts := []mount.MountInfo{{Major: 8, Minor: 16, Source: "/dev/sda"}, {Major: 8, Minor: 17, Source: "/dev/other-name"}}
	inUse, err := deviceInMountInfo(info, mounts, t.TempDir())
	if err != nil || !inUse {
		t.Fatalf("mounted device with another name: %v, %v", inUse, err)
	}
	inUse, err = deviceInMountInfo(testDeviceInfo{rdev: unix.Mkdev(8, 18)}, mounts, t.TempDir())
	if err != nil || inUse {
		t.Fatalf("unmounted device: %v, %v", inUse, err)
	}
}

func TestDeviceInMountInfoBtrfsAnonymousDevice(t *testing.T) {
	info := testDeviceInfo{rdev: unix.Mkdev(8, 17)}
	// Btrfs reports an anonymous filesystem device number, not the backing block device's Rdev.
	mounts := []mount.MountInfo{{
		Major:      0,
		Minor:      42,
		Root:       "/",
		MountPoint: "/other-target",
		FsType:     "btrfs",
		Source:     filepath.Join(t.TempDir(), "sdb1"),
	}}
	root := writeBtrfsDevices(t, [][]string{{"8:17"}})
	inUse, err := deviceInMountInfo(info, mounts, root)
	if err != nil || !inUse {
		t.Fatalf("mounted Btrfs device: %v, %v; want in use", inUse, err)
	}
}

func TestDeviceInMountInfoKnownDeviceBeforeUnresolvedBtrfs(t *testing.T) {
	mounts := []mount.MountInfo{
		{Major: 0, Minor: 42, FsType: "btrfs", Source: filepath.Join(t.TempDir(), "missing")},
		{Major: 8, Minor: 17, FsType: "ext4"},
	}
	if inUse, err := deviceInMountInfo(testDeviceInfo{rdev: unix.Mkdev(8, 17)}, mounts, t.TempDir()); err != nil || !inUse {
		t.Fatalf("known mounted device: %v, %v", inUse, err)
	}
}

func TestDeviceInMountInfoUnrelatedBtrfsWithInvisibleSource(t *testing.T) {
	mounts := []mount.MountInfo{{Major: 0, Minor: 42, FsType: "btrfs", Source: filepath.Join(t.TempDir(), "cr_root")}}
	root := writeBtrfsDevices(t, [][]string{{"254:0"}})
	if inUse, err := deviceInMountInfo(testDeviceInfo{rdev: unix.Mkdev(8, 17)}, mounts, root); err != nil || inUse {
		t.Fatalf("unmounted device with unrelated Btrfs mount: %v, %v", inUse, err)
	}
}

func TestDeviceInMountInfoBtrfsMissingSysfs(t *testing.T) {
	mounts := []mount.MountInfo{{Major: 0, Minor: 42, FsType: "btrfs"}}
	if inUse, err := deviceInMountInfo(testDeviceInfo{rdev: unix.Mkdev(8, 17)}, mounts, filepath.Join(t.TempDir(), "missing")); err == nil || inUse {
		t.Fatalf("unresolved Btrfs devices: %v, %v", inUse, err)
	}
}

func TestBtrfsDeviceInSysfs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		filesystems [][]string
		inUse       bool
		wantError   bool
	}{
		{name: "secondary member", filesystems: [][]string{{"8:16", "8:17"}}, inUse: true},
		{name: "unrelated filesystem", filesystems: [][]string{{"8:16", "8:18"}}},
		{name: "later filesystem", filesystems: [][]string{{"254:0"}, {"8:16", "8:17"}}, inUse: true},
		{name: "no filesystems", wantError: true},
		{name: "empty devices", filesystems: [][]string{{}}, wantError: true},
		{name: "missing device number", filesystems: [][]string{{"8:16", ""}}, wantError: true},
		{name: "malformed device number", filesystems: [][]string{{"8:16", "8:17extra"}}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeBtrfsDevices(t, tc.filesystems)
			inUse, err := btrfsDeviceInSysfs(unix.Mkdev(8, 17), root)
			if inUse != tc.inUse || (err != nil) != tc.wantError {
				t.Fatalf("btrfsDeviceInSysfs() = %v, %v; want inUse=%v, error=%v", inUse, err, tc.inUse, tc.wantError)
			}
		})
	}
}

func writeBtrfsDevices(t *testing.T, filesystems [][]string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"features", "debug"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for i, devices := range filesystems {
		path := filepath.Join(root, fmt.Sprintf("fs%d", i), "devices")
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		for j, number := range devices {
			device := filepath.Join(path, fmt.Sprintf("disk%d", j))
			if err := os.Mkdir(device, 0700); err != nil {
				t.Fatal(err)
			}
			if number != "" {
				if err := os.WriteFile(filepath.Join(device, "dev"), []byte(number+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return root
}
