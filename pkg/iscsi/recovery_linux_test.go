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
	inUse, err := deviceInMountInfo(info, mounts)
	if err != nil || !inUse {
		t.Fatalf("mounted device with another name: %v, %v", inUse, err)
	}
	inUse, err = deviceInMountInfo(testDeviceInfo{rdev: unix.Mkdev(8, 18)}, mounts)
	if err != nil || inUse {
		t.Fatalf("unmounted device: %v, %v", inUse, err)
	}
}
