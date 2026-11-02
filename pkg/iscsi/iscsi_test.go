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

package iscsi

import (
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

func TestGetISCSIInfoPortals(t *testing.T) {
	tests := []struct {
		name     string
		portals  *string
		expected []string
	}{
		{
			name:     "targetPortal only",
			expected: []string{"10.0.0.1:3260"},
		},
		{
			name:     "empty portals",
			portals:  strPtr("[]"),
			expected: []string{"10.0.0.1:3260"},
		},
		{
			name:     "targetPortal first, then portals in order",
			portals:  strPtr(`["10.0.0.3:3261", "10.0.0.2"]`),
			expected: []string{"10.0.0.1:3260", "10.0.0.3:3261", "10.0.0.2:3260"},
		},
		{
			name:     "portals repeating targetPortal and each other",
			portals:  strPtr(`["10.0.0.1", "10.0.0.2:3260", "10.0.0.1:3260", "10.0.0.2"]`),
			expected: []string{"10.0.0.1:3260", "10.0.0.2:3260"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			volumeContext := map[string]string{
				"targetPortal": "10.0.0.1",
				"iqn":          "iqn.2001-04.com.example:storage.disk1",
				"lun":          "0",
			}
			if test.portals != nil {
				volumeContext["portals"] = *test.portals
			}
			req := &csi.NodePublishVolumeRequest{VolumeId: "vol", VolumeContext: volumeContext}

			info, err := getISCSIInfo(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(info.Portals, test.expected) {
				t.Errorf("Portals = %v, expected %v", info.Portals, test.expected)
			}
			if c := buildISCSIConnector(info); !reflect.DeepEqual(c.TargetPortals, test.expected) {
				t.Errorf("connector TargetPortals = %v, expected %v", c.TargetPortals, test.expected)
			}
		})
	}
}

func TestGetISCSIInfoInvalidPortals(t *testing.T) {
	req := &csi.NodePublishVolumeRequest{
		VolumeId: "vol",
		VolumeContext: map[string]string{
			"targetPortal": "10.0.0.1",
			"iqn":          "iqn.2001-04.com.example:storage.disk1",
			"lun":          "0",
			"portals":      "10.0.0.2",
		},
	}
	if _, err := getISCSIInfo(req); err == nil {
		t.Error("expected an error for portals that are not a JSON list")
	}
}

func strPtr(s string) *string { return &s }
