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
	"path"

	iscsi_lib "github.com/kubernetes-csi/csi-lib-iscsi/iscsi"
	"k8s.io/klog/v2"
	"k8s.io/utils/mount"
)

type ISCSIUtil struct{}

func (util *ISCSIUtil) AttachDisk(b iscsiDiskMounter) (string, error) {
	devicePath, err := iscsi_lib.Connect(*b.connector)
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
		return "", fmt.Errorf("Heuristic determination of mount point failed:%v", err)
	}
	if !notMnt {
		klog.Infof("iscsi: %s already mounted", mntPath)
		return "", nil
	}

	if err := os.MkdirAll(mntPath, 0750); err != nil {
		klog.Errorf("iscsi: failed to mkdir %s, error", mntPath)
		return "", err
	}

	// Persist iscsi disk config to json file for DetachDisk path
	file := path.Join(mntPath, b.VolName+".json")
	err = iscsi_lib.PersistConnector(b.connector, file)
	if err != nil {
		klog.Errorf("failed to persist connection info: %v", err)
		klog.Errorf("disconnecting volume and failing the publish request because persistence files are required for reliable Unpublish")
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
		return fmt.Errorf("Error checking if path exists: %v", pathErr)
	} else if !pathExists {
		klog.Warningf("Warning: Unmount skipped because path does not exist: %v", targetPath)
		return nil
	}
	if err = c.mounter.Unmount(targetPath); err != nil {
		klog.Errorf("iscsi detach disk: failed to unmount: %s\nError: %v", targetPath, err)
		return err
	}
	cnt--
	if cnt != 0 {
		return nil
	}

	// load iscsi disk config from json file
	file := path.Join(targetPath, c.iscsiDisk.VolName+".json")
	connector, err := iscsi_lib.GetConnectorFromFile(file)
	if err != nil {
		klog.Errorf("iscsi detach disk: failed to get iscsi config from path %s Error: %v", targetPath, err)
		return err
	}

	if disConnectErr := iscsi_lib.Disconnect(connector.TargetIqn, connector.TargetPortals); disConnectErr != nil {
		klog.Warningf("Warning: Disconnect failed for IQN: %v", connector.TargetIqn)
	}
	if err := os.Remove(targetPath); err != nil {
		klog.Errorf("iscsi: failed to remove mount path Error: %v", err)
		return err
	}

	return nil
}
