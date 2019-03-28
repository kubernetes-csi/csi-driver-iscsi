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

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	iscsi_lib "github.com/kubernetes-csi/csi-lib-iscsi/iscsi"
	"github.com/kubernetes-csi/drivers/pkg/csi-common"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/util/mount"
	"k8s.io/utils/keymutex"
)

type nodeServer struct {
	*csicommon.DefaultNodeServer
}

var (
	devicePathMutex = keymutex.NewHashed(0)
)

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	mounter := &mount.SafeFormatAndMount{Interface: mount.New(""), Exec: mount.NewOsExec()}
	targetPath := req.GetTargetPath()

	devicePathMutex.LockKey(volumeID)
	defer devicePathMutex.UnlockKey(volumeID)

	notMnt, err := mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err = os.MkdirAll(targetPath, 0750); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			notMnt = true
		} else {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	if !notMnt {
		return &csi.NodePublishVolumeResponse{}, nil
	}

	c, err := buildISCSIConnector(req)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	p, err := iscsi_lib.Connect(*c)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if p == "" {
		return nil, status.Error(codes.Internal, fmt.Errorf("connect reported success, but no path returned").Error())
	}

	err = iscsi_lib.PersistConnector(c, iscsiPersistDir+volumeID+".json")
	if err != nil {
		klog.Errorf("failed to persist connection info: %v", err)
		klog.Errorf("disconnecting volume and failing the publish request because persistence files are required for reliable Unpublish")
		return nil, status.Error(codes.Internal, fmt.Errorf("unable to create persistence file for connection").Error())
	}

	klog.V(3).Infof("iscsi volume succesfully attached at: %s", p)
	fsType := req.GetVolumeCapability().GetMount().GetFsType()
	readOnly := req.GetReadonly()
	options := []string{}
	if readOnly {
		options = append(options, "ro")
	}

	if err := mounter.FormatAndMount(p, targetPath, fsType, options); err != nil {
		return nil, err
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	devicePathMutex.LockKey(volumeID)
	defer devicePathMutex.UnlockKey(volumeID)

	mounter := &mount.SafeFormatAndMount{Interface: mount.New(""), Exec: mount.NewOsExec()}
	notMnt, err := mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if notMnt {
		return nil, status.Error(codes.NotFound, "Volume not mounted")
	}

	devicePath, count, err := mount.GetDeviceNameFromMount(mounter, targetPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	klog.V(3).Infof("devicepath: %s", devicePath)

	// Unmounting the image
	err = mounter.Unmount(targetPath)
	if err != nil {

		return nil, status.Error(codes.Internal, err.Error())
	}

	count--
	if count != 0 {
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}

	c, err := iscsi_lib.GetConnectorFromFile(iscsiPersistDir + volumeID + ".json")
	if err != nil {

	}
	iscsi_lib.Disconnect(c.TargetIqn, c.TargetPortals)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return &csi.NodeStageVolumeResponse{}, nil
}
