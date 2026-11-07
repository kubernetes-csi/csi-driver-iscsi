/*
Copyright 2017 The Kubernetes Authors.

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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	iscsiLib "github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsilib"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	klog "k8s.io/klog/v2"

	"k8s.io/utils/mount"
)

type ISCSIUtil struct{}

var iscsiOperations sync.Mutex

func (util *ISCSIUtil) AttachDisk(b iscsiDiskMounter) (string, error) {
	iscsiOperations.Lock()
	defer iscsiOperations.Unlock()
	if b.connector == nil {
		return "", fmt.Errorf("connector is nil")
	}

	devicePath, err := (*b.connector).Connect()
	if err != nil {
		return "", err
	}
	if devicePath == "" {
		return "", fmt.Errorf("connect reported success, but no path returned")
	}
	// Mount device
	mntPath := b.targetPath
	notMnt, err := b.mounter.IsLikelyNotMountPoint(mntPath)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("heuristic determination of mount point failed:%v", err)
	}
	if !notMnt {
		klog.Infof("iscsi: %s already mounted", mntPath)
		return "", nil
	}

	if err := os.MkdirAll(mntPath, 0o750); err != nil {
		klog.Errorf("iscsi: failed to mkdir %s, error", mntPath)
		return "", err
	}

	// Persist iscsi disk config to json file for DetachDisk path
	iscsiInfoPath := getIscsiInfoPath(b.VolName)
	err = iscsiLib.PersistConnector(b.connector, iscsiInfoPath)
	if err != nil {
		klog.Errorf("failed to persist connection info: %v, disconnecting volume and failing the publish request because persistence files are required for reliable Unpublish", err)
		return "", fmt.Errorf("unable to create persistence file for connection")
	}

	var options []string

	if b.readOnly {
		options = append(options, "ro")
	} else {
		options = append(options, "rw")
	}
	options = append(options, b.mountOptions...)

	err = b.mounter.FormatAndMount(devicePath, mntPath, b.fsType, options)
	if err != nil {
		klog.Errorf("iscsi: failed to mount iscsi volume %s [%s] to %s, error %v", devicePath, b.fsType, mntPath, err)
	}

	return devicePath, err
}

func (util *ISCSIUtil) DetachDisk(c iscsiDiskUnmounter, targetPath string) error {
	iscsiOperations.Lock()
	defer iscsiOperations.Unlock()
	return detachDisk(c, targetPath, getIscsiInfoPath(c.VolName))
}

func detachDisk(c iscsiDiskUnmounter, targetPath, iscsiInfoPath string) error {
	device, _, err := mount.GetDeviceNameFromMount(c.mounter, targetPath)
	if err != nil {
		klog.Errorf("iscsi detach disk: failed to get device from mnt: %s\nError: %v", targetPath, err)
		return err
	}
	klog.Infof("loading ISCSI connection info from %s", iscsiInfoPath)
	data, err := os.ReadFile(iscsiInfoPath)
	if err != nil {
		if os.IsNotExist(err) {
			klog.Warningf("assuming that ISCSI connection is already closed")
			return nil
		}
		return status.Error(codes.Internal, err.Error())
	}
	connector := &iscsiLib.Connector{}
	if err := json.Unmarshal(data, connector); err != nil {
		return fmt.Errorf("invalid iSCSI connection record %s: %w", iscsiInfoPath, err)
	}
	if connector.TargetIqn == "" || len(connector.TargetPortals) == 0 {
		return fmt.Errorf("invalid iSCSI connection record %s", iscsiInfoPath)
	}
	portals, shared, err := unsharedPortals(iscsiInfoPath, connector)
	if err != nil {
		return err
	}
	if !connector.DevicesDetached {
		if device == "" {
			// shortcut: an unmounted device needs manual recovery until its identity can be verified.
			return fmt.Errorf("cannot safely resume iSCSI device removal for %s: device is no longer mounted", c.VolName)
		}
		connector, err = iscsiLib.GetConnectorFromFile(iscsiInfoPath)
		if err != nil {
			return err
		}
		if err = c.mounter.Unmount(targetPath); err != nil {
			return fmt.Errorf("failed to unmount %s: %w", targetPath, err)
		}
		inUse, err := deviceMounted(c.mounter, device)
		if err != nil {
			return err
		}
		if inUse {
			klog.Infof("the device is still in use: %s", device)
			return nil
		}
		klog.Info("detaching ISCSI device")
		if err = connector.DisconnectVolume(); err != nil {
			return fmt.Errorf("failed to disconnect iSCSI volume: %w", err)
		}
		connector.DevicesDetached = true
		if err = persistDetachedConnector(iscsiInfoPath, connector); err != nil {
			return err
		}
	} else if device != "" {
		return fmt.Errorf("cannot log out: device %s is mounted at %s", device, targetPath)
	}

	for _, portal := range portals {
		if err := iscsiLib.Logout(connector.TargetIqn, portal); err != nil {
			return err
		}
	}
	if !shared {
		if err := iscsiLib.DeleteDBEntry(connector.TargetIqn); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(targetPath); err != nil {
		klog.Errorf("iscsi: failed to remove mount path Error: %v", err)
	}
	err = os.Remove(iscsiInfoPath)
	if err != nil {
		return err
	}

	klog.Info("successfully detached ISCSI device")

	return nil
}

func persistDetachedConnector(path string, connector *iscsiLib.Connector) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".iscsi-detached-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(connector); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func unsharedPortals(infoPath string, connector *iscsiLib.Connector) ([]string, bool, error) {
	entries, err := os.ReadDir(filepath.Dir(infoPath))
	if err != nil {
		return nil, false, err
	}
	portals := slices.Clone(connector.TargetPortals)
	shared := false
	for _, entry := range entries {
		if entry.Name() == filepath.Base(infoPath) || !strings.HasPrefix(entry.Name(), "iscsi-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(infoPath), entry.Name()))
		if err != nil {
			return nil, false, err
		}
		var other iscsiLib.Connector
		if err := json.Unmarshal(data, &other); err != nil || other.TargetIqn == "" || len(other.TargetPortals) == 0 {
			return nil, false, fmt.Errorf("invalid iSCSI connection record %s", entry.Name())
		}
		if other.TargetIqn == connector.TargetIqn {
			shared = true
			portals = slices.DeleteFunc(portals, func(portal string) bool { return slices.Contains(other.TargetPortals, portal) })
		}
	}
	return portals, shared, nil
}

func deviceMounted(mounter mount.Interface, device string) (bool, error) {
	mounts, err := mounter.List()
	if err != nil {
		return false, err
	}
	resolved, _ := filepath.EvalSymlinks(device)
	for _, m := range mounts {
		mounted, _ := filepath.EvalSymlinks(m.Device)
		if m.Device == device || (resolved != "" && mounted == resolved) {
			return true, nil
		}
	}
	return false, nil
}

func getIscsiInfoPath(volumeID string) string {
	runPath := fmt.Sprintf("/var/run/%s", driverName)

	return fmt.Sprintf("%s/iscsi-%s.json", runPath, volumeID)
}
