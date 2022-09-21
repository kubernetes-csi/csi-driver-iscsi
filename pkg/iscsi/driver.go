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
	klog "k8s.io/klog/v2"
)

type driver struct {
	name     string
	nodeID   string
	version  string
	endpoint string
	cap      []*csi.VolumeCapability_AccessMode
	cscap    []*csi.ControllerServiceCapability
}

const (
	driverName = "iscsi.csi.k8s.io"
)

func NewDriver(nodeID, endpoint string) *driver {
	serVersion, err := GetVersionYAML(driverName)
	if err != nil {
		// default to 'canary`
		serVersion = "canary"
	}
	klog.V(1).Infof("driver: %s version: %s nodeID: %s endpoint: %s", driverName, serVersion, nodeID, endpoint)

	d := &driver{
		name:     driverName,
		version:  serVersion,
		nodeID:   nodeID,
		endpoint: endpoint,
	}

	if err := os.MkdirAll(fmt.Sprintf("/var/run/%s", driverName), 0o755); err != nil {
		panic(err)
	}
	d.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER})
	// iSCSI plugin does not support ControllerServiceCapability now.
	// If support is added, it should set to appropriate
	// ControllerServiceCapability RPC types.
	d.AddControllerServiceCapabilities([]csi.ControllerServiceCapability_RPC_Type{csi.ControllerServiceCapability_RPC_UNKNOWN})

	return d
}

func NewNodeServer(d *driver) *nodeServer {
	return &nodeServer{
		Driver: d,
	}
}

func (d *driver) Run() {
	s := NewNonBlockingGRPCServer()
	s.Start(d.endpoint,
		NewDefaultIdentityServer(d),
		// iSCSI plugin has not implemented ControllerServer
		// using default controllerserver.
		NewControllerServer(d),
		NewNodeServer(d))
	s.Wait()
}

func (d *driver) AddVolumeCapabilityAccessModes(vc []csi.VolumeCapability_AccessMode_Mode) []*csi.VolumeCapability_AccessMode {
	var vca []*csi.VolumeCapability_AccessMode
	for _, c := range vc {
		klog.Infof("enabling volume access mode: %v", c.String())
		vca = append(vca, &csi.VolumeCapability_AccessMode{Mode: c})
	}
	d.cap = vca
	return vca
}

func (d *driver) AddControllerServiceCapabilities(cl []csi.ControllerServiceCapability_RPC_Type) {
	var csc []*csi.ControllerServiceCapability

	for _, c := range cl {
		klog.Infof("enabling controller service capability: %v", c.String())
		csc = append(csc, NewControllerServiceCapability(c))
	}

	d.cscap = csc
}
