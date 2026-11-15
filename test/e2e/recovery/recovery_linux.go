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

// Package recovery exercises interrupted teardown through the real CSI socket.
package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	iscsiLib "github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsilib"
	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/workload"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/utils/mount"
)

// Run uses LUN 2 of a disposable target. The baseline owns LUN 1 and its shared session.
func Run(ctx context.Context, socket, portal, iqn, volume string, output io.Writer) (result error) {
	if !regexp.MustCompile(`^iscsi-pv-b-[a-z0-9-]+$`).MatchString(volume) || !strings.HasPrefix(iqn, "iqn.2026-01.com.test:storage-") || strings.ContainsAny(iqn, "/ \t\n") {
		return errors.New("recovery test requires an owned fixture IQN and recovery volume name")
	}
	if _, _, err := net.SplitHostPort(portal); err != nil {
		return err
	}
	record := filepath.Join("/var/run/iscsi.csi.k8s.io", "iscsi-"+volume+".json")
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		return fmt.Errorf("recovery record already exists or cannot be inspected: %s", record)
	}
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	node := csi.NewNodeClient(conn)
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	operation := workload.ProcessOperation(binary)
	unmount := func(ctx context.Context, path string) error {
		operationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return operation(operationCtx, "recovery-unmount", path)
	}
	directory, err := os.MkdirTemp("", "iscsi-btrfs-recovery-")
	if err != nil {
		return err
	}
	a, b := filepath.Join(directory, "a"), filepath.Join(directory, "b")
	if err = os.Mkdir(b, 0700); err != nil {
		return err
	}
	request := &csi.NodeUnpublishVolumeRequest{VolumeId: volume, TargetPath: a}
	published := false
	defer func() {
		// Remove the surviving reference before asking the driver to finish cleanup.
		for _, path := range []string{b, a} {
			entries, err := mount.ParseMountInfo("/proc/self/mountinfo")
			if err != nil {
				result = errors.Join(result, err)
				return
			}
			for _, entry := range entries {
				if entry.MountPoint == path {
					if err := unmount(context.Background(), path); err != nil {
						result = errors.Join(result, fmt.Errorf("cleanup unmount %s: %w", path, err))
						return
					}
				}
			}
		}
		if published {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := node.NodeUnpublishVolume(cleanup, request); err != nil {
				result = errors.Join(result, fmt.Errorf("recovery cleanup retained connection record: %w", err))
				return
			}
		}
		result = errors.Join(result, os.RemoveAll(directory))
		fmt.Fprintln(output, "Recovery cleanup: both test mounts removed; CSI cleanup completed")
	}()
	published = true // A timed-out RPC can still persist the connection and publish A.
	_, err = node.NodePublishVolume(ctx, &csi.NodePublishVolumeRequest{
		VolumeId: volume, TargetPath: a,
		VolumeContext: map[string]string{"targetPortal": portal, "iqn": iqn, "lun": "2", "portals": "[]", "discovery": "true"},
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "btrfs"}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
		},
	})
	if err != nil {
		return fmt.Errorf("recovery setup publish: %w", err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		return err
	}
	var connector iscsiLib.Connector
	if err = json.Unmarshal(data, &connector); err != nil {
		return err
	}
	if err = validateConnector(&connector, volume, portal, iqn); err != nil {
		return err
	}
	device := connector.MountTargetDevice
	info, err := os.Stat(device.GetPath())
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeType != os.ModeDevice {
		return errors.New("recovery target is not a block device")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("recovery target has no block device identity")
	}
	number := fmt.Sprintf("%d:%d", unix.Major(stat.Rdev), unix.Minor(stat.Rdev))
	sysfs := "/sys/dev/block/" + number
	if _, err = os.Stat(sysfs); err != nil {
		return err
	}
	wwid, err := device.WWID()
	if err != nil || wwid != connector.DeviceWWID {
		return fmt.Errorf("recovery WWID does not match saved identity: %q, %v", wwid, err)
	}
	byPath, err := matchingPath(info, portal, iqn)
	if err != nil {
		return err
	}
	source, _, err := mount.GetDeviceNameFromMount(mount.New(""), a)
	if err != nil {
		return err
	}
	mountedDevice, err := os.Stat(source)
	if err != nil || !os.SameFile(info, mountedDevice) {
		return fmt.Errorf("published mount does not use the recorded block device: %s", source)
	}
	if err = syscall.Mount(a, b, "", syscall.MS_BIND, ""); err != nil {
		return err
	}
	ioStep := func(mode string) error {
		operationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return operation(operationCtx, mode, b)
	}
	if err = ioStep("io-init"); err != nil {
		return fmt.Errorf("recovery setup I/O: %w", err)
	}
	if err = ioStep("io-step"); err != nil {
		return fmt.Errorf("recovery setup I/O: %w", err)
	}
	if err = unmount(ctx, a); err != nil {
		return err
	}
	entries, err := mount.ParseMountInfo("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	survivor, err := validateMounts(entries, a, b, stat.Rdev)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Recovery preconditions: A=%s unmounted; B=%s mounted; fs=%s mountinfo=%d:%d block=%s device=%s WWID=%s by-path=%s saved-single-path=true\n", a, b, survivor.FsType, survivor.Major, survivor.Minor, number, device.GetPath(), wwid, byPath)
	_, unpublishErr := node.NodeUnpublishVolume(ctx, request)
	fmt.Fprintf(output, "Recovery NodeUnpublishVolume: %v\n", unpublishErr)
	entries, err = mount.ParseMountInfo("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	if _, err = validateMounts(entries, a, b, stat.Rdev); err != nil {
		return fmt.Errorf("surviving mount changed during recovery: %w", err)
	}
	if _, err = os.Stat(sysfs); os.IsNotExist(err) {
		return fmt.Errorf("UNSAFE BTRFS RECOVERY: backing device %s (%s) disappeared while B remained mounted as %d:%d; NodeUnpublishVolume=%v", device.GetPath(), number, survivor.Major, survivor.Minor, unpublishErr)
	} else if err != nil {
		return err
	}
	if err = completedUnpublish(unpublishErr); err != nil {
		return err
	}
	current, err := os.Stat(device.GetPath())
	if err != nil || !os.SameFile(info, current) {
		return fmt.Errorf("recovery backing device changed: %v", err)
	}
	if err = ioStep("io-step"); err != nil {
		return fmt.Errorf("surviving Btrfs mount cannot complete fsync I/O after recovery: %w", err)
	}
	if _, err = os.Stat(record); err != nil {
		return fmt.Errorf("recovery lost connection record while B remained mounted: %w", err)
	}
	fmt.Fprintln(output, "Recovery PASS: backing device retained and surviving mount completed fsync I/O")
	return nil
}

// Unmount runs in a disposable child because a failed device can block unmount.
func Unmount(path string) error {
	dir := filepath.Dir(path)
	if filepath.Dir(dir) != os.TempDir() || !strings.HasPrefix(filepath.Base(dir), "iscsi-btrfs-recovery-") || (filepath.Base(path) != "a" && filepath.Base(path) != "b") {
		return errors.New("refusing to unmount a path outside the recovery fixture")
	}
	return syscall.Unmount(path, 0)
}

func completedUnpublish(err error) error {
	// This healthy fixture must identify the mounted device without refusing recovery.
	if err != nil {
		return fmt.Errorf("recovery RPC failed: %w", err)
	}
	return nil
}

func validateConnector(c *iscsiLib.Connector, volume, portal, iqn string) error {
	d := c.MountTargetDevice
	if c.VolumeName != volume || c.TargetIqn != iqn || c.Lun != 2 || len(c.TargetPortals) != 1 || c.TargetPortals[0] != portal || c.DevicesDetached || c.DeviceWWID == "" || d == nil || d.Type != "disk" || d.Transport != "iscsi" || len(d.Children) != 0 || len(c.Devices) != 1 || c.Devices[0].Name != d.Name || filepath.Base(d.Name) != d.Name {
		return errors.New("recovery setup did not record the expected single-path LUN and WWID")
	}
	return nil
}

func matchingPath(info os.FileInfo, portal, iqn string) (string, error) {
	entries, err := os.ReadDir("/dev/disk/by-path")
	if err != nil {
		return "", err
	}
	suffix := "ip-" + portal + "-iscsi-" + iqn + "-lun-2"
	found := ""
	for _, entry := range entries {
		if entry.Name() != suffix && !(strings.HasPrefix(entry.Name(), "pci-") && strings.HasSuffix(entry.Name(), "-"+suffix)) {
			continue
		}
		path := filepath.Join("/dev/disk/by-path", entry.Name())
		other, err := os.Stat(path)
		if err != nil || entry.Type()&os.ModeSymlink == 0 || !os.SameFile(info, other) {
			return "", fmt.Errorf("recovery target path does not identify the saved device: %s", path)
		}
		found = path
	}
	if found == "" {
		return "", errors.New("recovery target by-path link is missing")
	}
	return found, nil
}

func validateMounts(entries []mount.MountInfo, a, b string, rdev uint64) (mount.MountInfo, error) {
	var survivor mount.MountInfo
	for _, entry := range entries {
		if entry.MountPoint == a {
			return survivor, errors.New("recovery setup still has target A mounted")
		}
		if uint32(entry.Major) == unix.Major(rdev) && uint32(entry.Minor) == unix.Minor(rdev) {
			return survivor, errors.New("another mount matches the block device number and masks the regression")
		}
		if entry.MountPoint == b {
			survivor = entry
		}
	}
	if survivor.MountPoint == "" || survivor.FsType != "btrfs" || survivor.Major != 0 || survivor.Minor == 0 {
		return survivor, errors.New("recovery setup lacks a surviving Btrfs mount with an anonymous device number")
	}
	return survivor, nil
}
