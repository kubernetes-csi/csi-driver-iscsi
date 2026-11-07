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
	"syscall"

	"golang.org/x/sys/unix"
	"k8s.io/utils/mount"
)

func recoveryDeviceMounted(info os.FileInfo) (bool, error) {
	mounts, err := mount.ParseMountInfo("/proc/self/mountinfo")
	if err != nil {
		return false, fmt.Errorf("read mount information: %w", err)
	}
	return deviceInMountInfo(info, mounts)
}

func deviceInMountInfo(info os.FileInfo, mounts []mount.MountInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("missing block device number")
	}
	for _, m := range mounts {
		if uint32(m.Major) == unix.Major(stat.Rdev) && uint32(m.Minor) == unix.Minor(stat.Rdev) {
			return true, nil
		}
	}
	return false, nil
}
