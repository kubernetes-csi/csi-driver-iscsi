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
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"k8s.io/utils/mount"
)

func recoveryDeviceMounted(info os.FileInfo) (bool, error) {
	mounts, err := mount.ParseMountInfo("/proc/self/mountinfo")
	if err != nil {
		return false, fmt.Errorf("read mount information: %w", err)
	}
	return deviceInMountInfo(info, mounts, "/sys/fs/btrfs")
}

func deviceInMountInfo(info os.FileInfo, mounts []mount.MountInfo, btrfsRoot string) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("missing block device number")
	}
	hasBtrfs := false
	for _, m := range mounts {
		if uint32(m.Major) == unix.Major(stat.Rdev) && uint32(m.Minor) == unix.Minor(stat.Rdev) {
			return true, nil
		}
		hasBtrfs = hasBtrfs || m.FsType == "btrfs"
	}
	if hasBtrfs {
		// Btrfs mount numbers are anonymous; sysfs lists every mounted filesystem's devices.
		return btrfsDeviceInSysfs(stat.Rdev, btrfsRoot)
	}
	return false, nil
}

func btrfsDeviceInSysfs(rdev uint64, root string) (bool, error) {
	filesystems, err := os.ReadDir(root)
	if err != nil {
		return false, fmt.Errorf("read Btrfs filesystems: %w", err)
	}
	foundFilesystem := false
	for _, filesystem := range filesystems {
		if !filesystem.IsDir() || filesystem.Name() == "features" || filesystem.Name() == "debug" {
			continue
		}
		path := filepath.Join(root, filesystem.Name(), "devices")
		devices, err := os.ReadDir(path)
		if err != nil {
			return false, fmt.Errorf("read Btrfs backing devices: %w", err)
		}
		if len(devices) == 0 {
			return false, fmt.Errorf("no Btrfs backing devices in %s", path)
		}
		foundFilesystem = true
		for _, device := range devices {
			data, err := os.ReadFile(filepath.Join(path, device.Name(), "dev"))
			if err != nil {
				return false, fmt.Errorf("read Btrfs backing device number: %w", err)
			}
			var major, minor uint32
			number := strings.TrimSpace(string(data))
			if n, err := fmt.Sscanf(number, "%d:%d", &major, &minor); err != nil || n != 2 || number != fmt.Sprintf("%d:%d", major, minor) {
				return false, fmt.Errorf("invalid Btrfs backing device number %q", number)
			}
			if major == unix.Major(rdev) && minor == unix.Minor(rdev) {
				return true, nil
			}
		}
	}
	if !foundFilesystem {
		return false, fmt.Errorf("Btrfs mounts exist but no backing devices are visible in %s", root)
	}
	return false, nil
}
