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
	"fmt"
	"os"

	iscsiLib "github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsilib"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	klog "k8s.io/klog/v2"

	"k8s.io/utils/mount"
)

type ISCSIUtil struct{}

func (util *ISCSIUtil) AttachDisk(b iscsiDiskMounter) (string, error) {
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
	_, cnt, err := mount.GetDeviceNameFromMount(c.mounter, targetPath)
	if err != nil {
		klog.Errorf("iscsi detach disk: failed to get device from mnt: %s\nError: %v", targetPath, err)
		return err
	}
	if pathExists, pathErr := mount.PathExists(targetPath); pathErr != nil {
		return fmt.Errorf("error checking if path exists: %v", pathErr)
	} else if !pathExists {
		klog.Warningf("warning: Unmount skipped because path does not exist: %v", targetPath)
		return nil
	}
	iscsiInfoPath := getIscsiInfoPath(c.VolName)
	klog.Infof("loading ISCSI connection info from %s", iscsiInfoPath)
	connector, err := iscsiLib.GetConnectorFromFile(iscsiInfoPath)
	if err != nil {
		if os.IsNotExist(err) {
			klog.Warningf("assuming that ISCSI connection is already closed")
			return nil
		}
		return status.Error(codes.Internal, err.Error())
	}
	if err = c.mounter.Unmount(targetPath); err != nil {
		klog.Errorf("iscsi detach disk: failed to unmount: %s\nError: %v", targetPath, err)
		return err
	}
	cnt--
	if cnt != 0 {
		klog.Errorf("the device is in use : %d", cnt)
		return nil
	}

	klog.Info("detaching ISCSI device")
	err = connector.DisconnectVolume()
	if err != nil {
		klog.Errorf("iscsi detach disk: failed to disconnect volume Error: %v", err)
		return err
	}

	iscsiLib.Disconnect(connector.TargetIqn, connector.TargetPortals)
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

func getIscsiInfoPath(volumeID string) string {
	runPath := fmt.Sprintf("/var/run/%s", driverName)

	return fmt.Sprintf("%s/iscsi-%s.json", runPath, volumeID)
}
