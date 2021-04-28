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
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	iscsi_lib "github.com/kubernetes-csi/csi-lib-iscsi/iscsi"
	"k8s.io/kubernetes/pkg/volume/util"
	"k8s.io/utils/exec"
	"k8s.io/utils/mount"
)

func getISCSIInfo(req *csi.NodePublishVolumeRequest) (*iscsiDisk, error) {
	volName := req.GetVolumeId()
	tp := req.GetVolumeContext()["targetPortal"]
	iqn := req.GetVolumeContext()["iqn"]
	lun := req.GetVolumeContext()["lun"]
	if tp == "" || iqn == "" || lun == "" {
		return nil, fmt.Errorf("iSCSI target information is missing")
	}

	portalList := req.GetVolumeContext()["portals"]
	secretParams := req.GetVolumeContext()["secret"]
	secret := parseSecret(secretParams)
	sessionSecret, err := parseSessionSecret(secret)
	if err != nil {
		return nil, err
	}
	discoverySecret, err := parseDiscoverySecret(secret)
	if err != nil {
		return nil, err
	}

	portal := portalMounter(tp)
	var bkportal []string
	bkportal = append(bkportal, portal)

	portals := []string{}
	if err := json.Unmarshal([]byte(portalList), &portals); err != nil {
		return nil, err
	}

	for _, portal := range portals {
		bkportal = append(bkportal, portalMounter(portal))
	}

	iface := req.GetVolumeContext()["iscsiInterface"]
	initiatorName := req.GetVolumeContext()["initiatorName"]
	chapDiscovery := false
	if req.GetVolumeContext()["discoveryCHAPAuth"] == "true" {
		chapDiscovery = true
	}

	chapSession := false
	if req.GetVolumeContext()["sessionCHAPAuth"] == "true" {
		chapSession = true
	}

	var lunVal int32
	if lun != "" {
		l, err := strconv.Atoi(lun)
		if err != nil {
			return nil, err
		}
		lunVal = int32(l)
	}

	return &iscsiDisk{
		VolName:         volName,
		Portals:         bkportal,
		Iqn:             iqn,
		lun:             lunVal,
		Iface:           iface,
		chapDiscovery:   chapDiscovery,
		chapSession:     chapSession,
		secret:          secret,
		sessionSecret:   sessionSecret,
		discoverySecret: discoverySecret,
		InitiatorName:   initiatorName}, nil
}

func buildISCSIConnector(iscsiInfo *iscsiDisk) *iscsi_lib.Connector {
	c := iscsi_lib.Connector{
		VolumeName:    iscsiInfo.VolName,
		TargetIqn:     iscsiInfo.Iqn,
		TargetPortals: iscsiInfo.Portals,
		Multipath:     len(iscsiInfo.Portals) > 1,
	}

	if iscsiInfo.sessionSecret != (iscsi_lib.Secrets{}) {
		c.SessionSecrets = iscsiInfo.sessionSecret
		if iscsiInfo.discoverySecret != (iscsi_lib.Secrets{}) {
			c.DiscoverySecrets = iscsiInfo.discoverySecret
		}
	}

	return &c
}

func getISCSIDiskMounter(iscsiInfo *iscsiDisk, req *csi.NodePublishVolumeRequest) *iscsiDiskMounter {
	readOnly := req.GetReadonly()
	fsType := req.GetVolumeCapability().GetMount().GetFsType()
	mountOptions := req.GetVolumeCapability().GetMount().GetMountFlags()

	return &iscsiDiskMounter{
		iscsiDisk:    iscsiInfo,
		fsType:       fsType,
		readOnly:     readOnly,
		mountOptions: mountOptions,
		mounter:      &mount.SafeFormatAndMount{Interface: mount.New(""), Exec: exec.New()},
		exec:         exec.New(),
		targetPath:   req.GetTargetPath(),
		deviceUtil:   util.NewDeviceHandler(util.NewIOHandler()),
		connector:    buildISCSIConnector(iscsiInfo),
	}
}

func getISCSIDiskUnmounter(req *csi.NodeUnpublishVolumeRequest) *iscsiDiskUnmounter {
	return &iscsiDiskUnmounter{
		iscsiDisk: &iscsiDisk{
			VolName: req.GetVolumeId(),
		},
		mounter: mount.New(""),
		exec:    exec.New(),
	}
}

func portalMounter(portal string) string {
	if !strings.Contains(portal, ":") {
		portal = portal + ":3260"
	}
	return portal
}

func parseSecret(secretParams string) map[string]string {
	var secret map[string]string
	if err := json.Unmarshal([]byte(secretParams), &secret); err != nil {
		return nil
	}
	return secret
}

func parseSessionSecret(secretParams map[string]string) (iscsi_lib.Secrets, error) {
	var ok bool
	secret := iscsi_lib.Secrets{}

	if len(secretParams) == 0 {
		return secret, nil
	}

	if secret.UserName, ok = secretParams["node.session.auth.username"]; !ok {
		return iscsi_lib.Secrets{}, fmt.Errorf("node.session.auth.username not found in secret")
	}
	if secret.Password, ok = secretParams["node.session.auth.password"]; !ok {
		return iscsi_lib.Secrets{}, fmt.Errorf("node.session.auth.password not found in secret")
	}
	if secret.UserNameIn, ok = secretParams["node.session.auth.username_in"]; !ok {
		return iscsi_lib.Secrets{}, fmt.Errorf("node.session.auth.username_in not found in secret")
	}
	if secret.PasswordIn, ok = secretParams["node.session.auth.password_in"]; !ok {
		return iscsi_lib.Secrets{}, fmt.Errorf("node.session.auth.password_in not found in secret")
	}

	secret.SecretsType = "chap"
	return secret, nil
}

func parseDiscoverySecret(secretParams map[string]string) (iscsi_lib.Secrets, error) {
	var ok bool
	secret := iscsi_lib.Secrets{}

	if len(secretParams) == 0 {
		return secret, nil
	}

	if secret.UserName, ok = secretParams["node.sendtargets.auth.username"]; !ok {
		return iscsi_lib.Secrets{}, fmt.Errorf("node.sendtargets.auth.username not found in secret")
	}
	if secret.Password, ok = secretParams["node.sendtargets.auth.password"]; !ok {
		return iscsi_lib.Secrets{}, fmt.Errorf("node.sendtargets.auth.password not found in secret")
	}
	if secret.UserNameIn, ok = secretParams["node.sendtargets.auth.username_in"]; !ok {
		return iscsi_lib.Secrets{}, fmt.Errorf("node.sendtargets.auth.username_in not found in secret")
	}
	if secret.PasswordIn, ok = secretParams["node.sendtargets.auth.password_in"]; !ok {
		return iscsi_lib.Secrets{}, fmt.Errorf("node.sendtargets.auth.password_in not found in secret")
	}

	secret.SecretsType = "chap"
	return secret, nil
}

type iscsiDisk struct {
	Portals         []string
	Iqn             string
	lun             int32
	Iface           string
	chapDiscovery   bool
	chapSession     bool
	secret          map[string]string
	sessionSecret   iscsi_lib.Secrets
	discoverySecret iscsi_lib.Secrets
	InitiatorName   string
	VolName         string
}

type iscsiDiskMounter struct {
	*iscsiDisk
	readOnly     bool
	fsType       string
	mountOptions []string
	mounter      *mount.SafeFormatAndMount
	exec         exec.Interface
	deviceUtil   util.DeviceUtil
	targetPath   string
	connector    *iscsi_lib.Connector
}

type iscsiDiskUnmounter struct {
	*iscsiDisk
	mounter mount.Interface
	exec    exec.Interface
}
