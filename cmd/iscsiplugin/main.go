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

package main

import (
	"flag"
	"os"

	"github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsi"
	"github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsilib"
	klog "k8s.io/klog/v2"
)

var (
	endpoint      = flag.String("endpoint", "unix:///csi/csi.sock", "CSI endpoint")
	nodeID        = flag.String("nodeid", "", "node id")
	loginTimeout  = flag.Duration("iscsi-login-timeout", iscsilib.DefaultLoginTimeout, "Timeout for each iscsiadm login command (0 uses the default)")
	logoutTimeout = flag.Duration("iscsi-logout-timeout", iscsilib.DefaultLogoutTimeout, "Timeout for each iscsiadm logout command (0 uses the default)")
)

func init() {
	klog.InitFlags(nil)
}

func main() {
	flag.Parse()
	handle()
	os.Exit(0)
}

func handle() {
	d := iscsi.NewDriver(*nodeID, *endpoint)
	if err := d.SetCommandTimeouts(iscsilib.CommandTimeouts{Login: *loginTimeout, Logout: *logoutTimeout}); err != nil {
		klog.Exitf("Invalid iSCSI command timeouts: %v", err)
	}
	d.Run()
}
