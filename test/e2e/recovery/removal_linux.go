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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	iscsiLib "github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsilib"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func removalRecovery(ctx context.Context, node csi.NodeClient, request *csi.NodeUnpublishVolumeRequest, connector *iscsiLib.Connector, record, byPath, sysfs, directory string, output io.Writer) (result error) {
	restore, err := injectRemovalFault(record, connector.MountTargetDevice.GetPath(), directory)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, restore()) }()
	_, err = node.NodeUnpublishVolume(ctx, request)
	fmt.Fprintf(output, "Removal first unpublish: %v\n", err)
	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "device or resource busy") {
		return fmt.Errorf("expected persistence rename failure after removal, got %v", err)
	}
	if err := waitForRemoval(ctx, []string{sysfs, connector.MountTargetDevice.GetPath(), byPath}); err != nil {
		return err
	}
	data, err := os.ReadFile(record)
	if err != nil {
		return err
	}
	var saved iscsiLib.Connector
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	if err := validateConnector(&saved, connector.VolumeName, connector.TargetPortals[0], connector.TargetIqn); err != nil {
		return fmt.Errorf("removal did not retain its unfinished connection record: %w", err)
	}
	fmt.Fprintf(output, "Removal preconditions: device=%s and by-path=%s absent; devices_detached=false; WWID=%s\n", connector.MountTargetDevice.GetPath(), byPath, saved.DeviceWWID)
	if err := restore(); err != nil {
		return err
	}
	var failures []error
	for attempt := 1; attempt <= 3; attempt++ {
		_, err := node.NodeUnpublishVolume(ctx, request)
		fmt.Fprintf(output, "Removal retry %d: %v\n", attempt, err)
		if err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		return fmt.Errorf("removal recovery failed after device deletion: %w", errors.Join(failures...))
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		return fmt.Errorf("connection record remains after removal recovery: %v", err)
	}
	fmt.Fprintln(output, "Removal recovery PASS: retry removed the connection record; repeated unpublish succeeded")
	return nil
}

// Pin the record after the flush starts so only the post-deletion rename fails with EBUSY.
func injectRemovalFault(record, device, directory string) (func() error, error) {
	blockdev, err := exec.LookPath("blockdev")
	if err != nil {
		return nil, err
	}
	blockdev, err = filepath.EvalSymlinks(blockdev)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(blockdev)
	if err != nil {
		return nil, err
	}
	realBlockdev := filepath.Join(directory, "blockdev-real")
	if err := os.WriteFile(realBlockdev, data, 0700); err != nil {
		return nil, err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := fmt.Sprintf("#!/bin/sh\nset -eu\n%s \"$@\"\nif [ \"$#\" = 2 ] && [ \"$1\" = --flushbufs ] && [ \"$2\" = %s ]; then\n  mount --bind %s %s\nfi\n", quote(realBlockdev), quote(device), quote(record), quote(record))
	wrapper := filepath.Join(directory, "blockdev-fault")
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		return nil, err
	}
	if err := syscall.Mount(wrapper, blockdev, "", syscall.MS_BIND, ""); err != nil {
		return nil, err
	}
	restore := func() error {
		var result error
		for _, path := range []string{blockdev, record} {
			if err := syscall.Unmount(path, 0); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOENT) {
				result = errors.Join(result, err)
			}
		}
		return result
	}
	return restore, nil
}

func waitForRemoval(ctx context.Context, paths []string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, path := range paths {
		for {
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				break
			} else if err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("device removal did not complete for %s: %w", path, ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return nil
}
