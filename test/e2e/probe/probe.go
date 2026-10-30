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

// Package probe observes a node through its read-only host-root mount.
package probe

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Error distinguishes residual storage (1) from unobservable or invalid state (2).
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return e.Message }

func observationError(format string, args ...any) error {
	return &Error{Code: 2, Message: fmt.Sprintf(format, args...)}
}

// Prerequisites checks existing node state without executing a host program or
// changing packages, kernel modules, services, or host files.
func Prerequisites(root string) error { return PrerequisitesOS(root, runtime.GOOS) }

// PrerequisitesOS permits deterministic Linux/non-Linux fixture checks.
func PrerequisitesOS(root, osName string) error {
	missing := func(message string) error { return &Error{Code: 1, Message: "Node prerequisite failed: " + message} }
	if osName != "linux" {
		return missing("Linux nodes are required")
	}
	found := false
	for _, relative := range []string{"sbin/iscsiadm", "usr/sbin/iscsiadm", "bin/iscsiadm", "usr/bin/iscsiadm", "usr/local/sbin/iscsiadm", "usr/local/bin/iscsiadm"} {
		if hostExecutable(root, relative) {
			found = true
			break
		}
	}
	if !found {
		return missing("iscsiadm is missing; prepare the disposable node first")
	}
	processes, err := filepath.Glob(filepath.Join(root, "proc", "[0-9]*", "comm"))
	if err != nil {
		return missing("cannot observe node processes")
	}
	running := map[string]bool{}
	for _, path := range processes {
		data, err := os.ReadFile(path)
		if err == nil {
			running[strings.TrimSpace(string(data))] = true
		}
	}
	if !running["iscsid"] {
		return missing("iscsid is not running")
	}
	info, err := os.Stat(filepath.Join(root, "sys/class/iscsi_transport/tcp"))
	if err != nil || !info.IsDir() {
		return missing("iSCSI TCP kernel transport is unavailable")
	}
	if !running["systemd-udevd"] && !running["udevd"] {
		return missing("udev is not running; device discovery requires udev")
	}
	return nil
}

// Resolve host absolute symlinks relative to the mounted host root, rather than
// accidentally consulting a similarly named program inside the probe image.
func hostExecutable(root, relative string) bool {
	pending := strings.Split(filepath.ToSlash(relative), "/")
	resolved := []string{}
	links := 0
	var info os.FileInfo
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			continue
		}
		path := filepath.Join(append([]string{root}, append(resolved, part)...)...)
		var err error
		info, err = os.Lstat(path)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return false
			}
			target, err := os.Readlink(path)
			if err != nil {
				return false
			}
			if filepath.IsAbs(target) {
				resolved = nil
			}
			pending = append(strings.Split(filepath.ToSlash(target), "/"), pending...)
		} else {
			resolved = append(resolved, part)
		}
	}
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

var safeName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
var safeIQN = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+$`)
var digits = regexp.MustCompile(`^[0-9]+$`)

// VolumeOptions identifies a run's publication and previously observed devices.
// Snapshot/check operate on the whole target; their -lun variants allow another
// LUN to retain the shared target session.
type VolumeOptions struct {
	Root, IQN, Volume, LUN string
	Tracked                []string
}

// Volume returns a stable device snapshot or verifies a storage observation.
func Volume(mode string, options VolumeOptions) ([]string, error) {
	switch mode {
	case "snapshot", "check", "snapshot-lun", "check-lun", "published":
	default:
		return nil, observationError("Invalid cleanup check mode")
	}
	if !safeName.MatchString(options.Volume) {
		return nil, observationError("Invalid cleanup volume identifier")
	}
	if !safeIQN.MatchString(options.IQN) {
		return nil, observationError("Invalid target identifier")
	}
	if strings.HasSuffix(mode, "-lun") && !digits.MatchString(options.LUN) {
		return nil, observationError("Invalid LUN identifier")
	}
	for _, name := range options.Tracked {
		if !safeName.MatchString(name) {
			return nil, observationError("Invalid tracked block device")
		}
	}
	root := options.Root
	if root == "" {
		root = "/"
	}
	mounted, err := publicationMounted(filepath.Join(root, "proc/1/mountinfo"), options.Volume)
	if err != nil {
		return nil, err
	}
	if _, err := os.ReadDir(filepath.Join(root, "sys/class/block")); err != nil {
		return nil, observationError("Cannot observe node block devices")
	}
	if mode == "published" {
		if !mounted {
			return nil, observationError("Expected A publication mount is not observable")
		}
		return nil, nil
	}
	sessions, err := os.ReadDir(filepath.Join(root, "sys/class/iscsi_session"))
	if err != nil {
		return nil, observationError("Cannot observe node iSCSI sessions")
	}
	residual := []string{}
	devices := map[string]bool{}
	for _, session := range sessions {
		if !strings.HasPrefix(session.Name(), "session") {
			continue
		}
		directory := filepath.Join(root, "sys/class/iscsi_session", session.Name())
		identity, err := os.ReadFile(filepath.Join(directory, "targetname"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} // A disappearing session is normal during unpublish.
		if err != nil {
			return nil, observationError("Cannot observe iSCSI session identity")
		}
		if strings.TrimSpace(string(identity)) != options.IQN {
			continue
		}
		if mode == "check" {
			residual = append(residual, "Run iSCSI session remains on node")
		}
		if strings.HasPrefix(mode, "snapshot") {
			paths, err := filepath.Glob(filepath.Join(directory, "device/target*/*/block/*"))
			if err != nil {
				return nil, observationError("Cannot observe iSCSI target devices")
			}
			for _, path := range paths {
				if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
					continue
				} else if err != nil {
					return nil, observationError("Cannot observe iSCSI target devices")
				}
				if mode == "snapshot-lun" {
					address := filepath.Base(filepath.Dir(filepath.Dir(path)))
					lun := address[strings.LastIndex(address, ":")+1:]
					if !sameLUN(lun, options.LUN) {
						continue
					}
				}
				devices[filepath.Base(path)] = true
			}
		}
	}
	if strings.HasPrefix(mode, "snapshot") {
		result := make([]string, 0, len(devices))
		for device := range devices {
			result = append(result, device)
		}
		sort.Strings(result)
		return result, nil
	}
	if mounted {
		residual = append(residual, "Run publication mount remains in node mount namespace")
	}
	paths, err := os.ReadDir(filepath.Join(root, "dev/disk/by-path"))
	if err != nil {
		return nil, observationError("Cannot observe iSCSI device paths")
	}
	marker := "-iscsi-" + options.IQN + "-lun-"
	for _, path := range paths {
		_, lun, matched := strings.Cut(path.Name(), marker)
		if !matched {
			continue
		}
		if mode == "check-lun" && !sameLUN(lun, options.LUN) {
			continue
		}
		residual = append(residual, "Run iSCSI device path remains on node")
	}
	for _, device := range options.Tracked {
		_, err := os.Lstat(filepath.Join(root, "sys/class/block", device))
		if err == nil {
			residual = append(residual, "Previously observed run block device remains on node")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, observationError("Cannot observe captured block device")
		}
	}
	if len(residual) > 0 {
		return nil, &Error{Code: 1, Message: strings.Join(residual, "; ")}
	}
	return nil, nil
}

func sameLUN(a, b string) bool {
	if !digits.MatchString(a) || !digits.MatchString(b) {
		return false
	}
	x, errA := strconv.ParseUint(a, 10, 64)
	y, errB := strconv.ParseUint(b, 10, 64)
	return errA == nil && errB == nil && x == y
}

func publicationMounted(path, volume string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, observationError("Cannot observe node mount namespace")
	}
	// Closing this read-only mountinfo handle cannot change the observation.
	defer func() { _ = file.Close() }()
	marker := "/volumes/kubernetes.io~csi/" + volume + "/"
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	mounted := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			return false, observationError("Cannot parse node mount namespace")
		}
		if strings.Contains(fields[4], marker) {
			mounted = true
		}
	}
	if scanner.Err() != nil {
		return false, observationError("Cannot read node mount namespace")
	}
	return mounted, nil
}
