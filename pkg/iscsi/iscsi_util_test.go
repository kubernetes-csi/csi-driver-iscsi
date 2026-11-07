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
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	iscsiLib "github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsilib"
	"k8s.io/utils/mount"
)

func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test requires Unix executables or symbolic links")
	}
}

func TestDeviceMounted(t *testing.T) {
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "/dev/sda", Path: "/other"}})
	for _, test := range []struct {
		device string
		want   bool
	}{{"/dev/sda", true}, {"/dev/sdb", false}} {
		got, err := deviceMounted(mounter, test.device)
		if err != nil || got != test.want {
			t.Fatalf("deviceMounted(%q) = %v, %v; want %v", test.device, got, err, test.want)
		}
	}
}

func TestDeviceMountedWithSymlinkAliases(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	device := filepath.Join(dir, "device")
	if err := os.WriteFile(device, nil, 0600); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, link := range []string{first, second} {
		if err := os.Symlink(device, link); err != nil {
			t.Fatal(err)
		}
	}
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: second, Path: "/other"}})
	if inUse, err := deviceMounted(mounter, first); err != nil || !inUse {
		t.Fatalf("deviceMounted with aliases = %v, %v", inUse, err)
	}
}

func TestDetachDiskRetriesLogout(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "iscsi-volume.json")
	connector := &iscsiLib.Connector{TargetIqn: "iqn.test:target", TargetPortals: []string{"127.0.0.1:3260"}, DevicesDetached: true}
	if err := persistDetachedConnector(infoPath, connector); err != nil {
		t.Fatal(err)
	}
	fail := filepath.Join(dir, "fail")
	if err := os.WriteFile(fail, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ISCSI_TEST_FAIL", fail)
	script := `#!/bin/sh
if [ "$2" = session ]; then
  if [ -e "$ISCSI_TEST_FAIL" ]; then echo 'tcp: [1] 127.0.0.1:3260,1 iqn.test:target'; exit 0; fi
  exit 21
fi
if [ "$7" = -u ]; then
  if [ -e "$ISCSI_TEST_FAIL" ]; then exit 1; fi
  exit 21
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "iscsiadm"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	c := iscsiDiskUnmounter{iscsiDisk: &iscsiDisk{VolName: "volume"}, mounter: mount.NewFakeMounter(nil)}
	target := filepath.Join(dir, "missing-mount")
	if err := detachDisk(c, target, infoPath); err == nil {
		t.Fatal("logout failure was ignored")
	}
	if _, err := os.Stat(infoPath); err != nil {
		t.Fatalf("connection record was lost: %v", err)
	}
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	if err := detachDisk(c, target, infoPath); err != nil {
		t.Fatalf("retry after logout completed: %v", err)
	}
	if _, err := os.Stat(infoPath); !os.IsNotExist(err) {
		t.Fatalf("connection record remains after cleanup: %v", err)
	}
}

func TestDetachDiskRejectsAmbiguousPartialCleanup(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "iscsi-volume.json")
	connector := iscsiLib.Connector{TargetIqn: "iqn.test:target", TargetPortals: []string{"127.0.0.1:3260"}}
	data, err := json.Marshal(connector)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	c := iscsiDiskUnmounter{iscsiDisk: &iscsiDisk{VolName: "volume"}, mounter: mount.NewFakeMounter(nil)}
	if err := detachDisk(c, filepath.Join(dir, "missing"), infoPath); err == nil || !strings.Contains(err.Error(), "cannot safely resume") {
		t.Fatalf("expected a safe cleanup error, got %v", err)
	}
	if _, err := os.Stat(infoPath); err != nil {
		t.Fatalf("connection record was lost: %v", err)
	}
}

func TestRecoveryTargetPath(t *testing.T) {
	skipWindows(t)
	for _, tc := range []struct {
		name      string
		change    func(*testing.T, *iscsiLib.Connector, string, string)
		wantError bool
	}{
		{name: "matching target"},
		{name: "other target with same WWID", wantError: true, change: func(t *testing.T, c *iscsiLib.Connector, _, _ string) { c.TargetIqn = "iqn.test:other" }},
		{name: "wrong LUN", wantError: true, change: func(t *testing.T, c *iscsiLib.Connector, _, _ string) { c.Lun = 2 }},
		{name: "ambiguous devices", wantError: true, change: func(t *testing.T, _ *iscsiLib.Connector, dir, link string) {
			other := filepath.Join(dir, "other")
			if err := os.WriteFile(other, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, filepath.Join(filepath.Dir(link), "pci-0000:00:01.0-"+filepath.Base(link))); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "PCI path", change: func(t *testing.T, _ *iscsiLib.Connector, _, link string) {
			if err := os.Rename(link, filepath.Join(filepath.Dir(link), "pci-0000:00:01.0-"+filepath.Base(link))); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			device := filepath.Join(dir, "device")
			if err := os.WriteFile(device, nil, 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(device)
			if err != nil {
				t.Fatal(err)
			}
			byPath := filepath.Join(dir, "by-path")
			if err := os.Mkdir(byPath, 0755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(byPath, "ip-192.0.2.1:3260-iscsi-iqn.test:target-lun-1")
			if err := os.Symlink(device, link); err != nil {
				t.Fatal(err)
			}
			connector := &iscsiLib.Connector{TargetIqn: "iqn.test:target", TargetPortals: []string{"192.0.2.1:3260"}, Lun: 1, MountTargetDevice: &iscsiLib.Device{Name: "old-device"}}
			if tc.change != nil {
				tc.change(t, connector, dir, link)
			}
			path, err := recoveryTargetPath(connector, byPath)
			if (err != nil) != tc.wantError {
				t.Fatalf("recoveryTargetPath() = %q, %v; want error %v", path, err, tc.wantError)
			}
			if err == nil {
				resolved, err := os.Stat(path)
				if err != nil || !os.SameFile(info, resolved) {
					t.Fatalf("recovery path does not identify current device: %v", err)
				}
			}
		})
	}
}

func TestDetachDiskRejectsMissingRecoveryTarget(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "iscsi-volume.json")
	device := iscsiLib.Device{Name: "old-device", Type: "disk", Transport: "iscsi"}
	connector := &iscsiLib.Connector{
		TargetIqn: "iqn.test:" + filepath.Base(dir), TargetPortals: []string{"192.0.2.1:3260"}, Lun: 1,
		DeviceWWID: "naa.123", MountTargetDevice: &device, Devices: []iscsiLib.Device{device},
	}
	if err := connector.Persist(infoPath); err != nil {
		t.Fatal(err)
	}
	c := iscsiDiskUnmounter{iscsiDisk: &iscsiDisk{VolName: "volume"}, mounter: mount.NewFakeMounter(nil)}
	if err := detachDisk(c, filepath.Join(dir, "missing"), infoPath); err == nil {
		t.Fatal("missing target path was accepted")
	}
	if _, err := os.Stat(infoPath); err != nil {
		t.Fatalf("connection record was lost: %v", err)
	}
}

func TestVerifyRecoveryWWID(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scsi_id"), []byte("#!/bin/sh\nif [ \"$SCSI_TEST_OUTPUT\" = fail ]; then exit 1; fi\nprintf '%s' \"$SCSI_TEST_OUTPUT\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	connector := &iscsiLib.Connector{DeviceWWID: "naa.123", MountTargetDevice: &iscsiLib.Device{Name: "test", Type: "disk"}}
	for _, tc := range []struct {
		name, output string
		wantError    bool
	}{{"same", "naa.123\n", false}, {"changed", "naa.456\n", true}, {"empty", "", true}, {"command failure", "fail", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SCSI_TEST_OUTPUT", tc.output)
			err := verifyRecoveryWWID(connector)
			if (err != nil) != tc.wantError {
				t.Fatalf("verifyRecoveryWWID() = %v; want error %v", err, tc.wantError)
			}
		})
	}
}

func TestRecordDeviceWWID(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$SCSI_TEST_OUTPUT\" = fail ]; then exit 1; fi\nprintf '%s' \"$SCSI_TEST_OUTPUT\"\n"
	if err := os.WriteFile(filepath.Join(dir, "scsi_id"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		name, output, deviceType, want string
	}{{"available", "naa.123\n", "disk", "naa.123"}, {"unavailable", "fail", "disk", ""}, {"multipath", "naa.123", "mpath", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SCSI_TEST_OUTPUT", tc.output)
			device := iscsiLib.Device{Name: "test", Type: tc.deviceType}
			connector := &iscsiLib.Connector{DeviceWWID: "stale", MountTargetDevice: &device, Devices: []iscsiLib.Device{device}}
			recordDeviceWWID(connector)
			path := filepath.Join(t.TempDir(), "connector.json")
			if err := connector.Persist(path); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var saved iscsiLib.Connector
			if err := json.Unmarshal(data, &saved); err != nil || saved.DeviceWWID != tc.want {
				t.Fatalf("saved WWID = %q, %v; want %q", saved.DeviceWWID, err, tc.want)
			}
		})
	}
}

func TestDetachDiskPreservesSharedPortal(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "iscsi-volume.json")
	connector := &iscsiLib.Connector{TargetIqn: "iqn.test:target", TargetPortals: []string{"one:3260", "two:3260"}, DevicesDetached: true}
	if err := persistDetachedConnector(infoPath, connector); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "iscsi-other.json"), []byte(`{"target_iqn":"iqn.test:target","target_portal":["two:3260"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "commands")
	t.Setenv("ISCSI_TEST_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "iscsiadm"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ISCSI_TEST_LOG\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	c := iscsiDiskUnmounter{iscsiDisk: &iscsiDisk{VolName: "volume"}, mounter: mount.NewFakeMounter(nil)}
	if err := detachDisk(c, filepath.Join(dir, "missing"), infoPath); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(commands); !strings.Contains(got, "-p one:3260 -u") || strings.Contains(got, "two:3260") || strings.Contains(got, "-o delete") {
		t.Fatalf("unexpected commands: %s", got)
	}
}

func TestDetachDiskResumesPortalLogouts(t *testing.T) {
	skipWindows(t)
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "iscsi-volume.json")
	connector := &iscsiLib.Connector{TargetIqn: "iqn.test:target", TargetPortals: []string{"127.0.0.1:3260", "127.0.0.2:3260"}, DevicesDetached: true}
	if err := persistDetachedConnector(infoPath, connector); err != nil {
		t.Fatal(err)
	}
	log, fail := filepath.Join(dir, "commands"), filepath.Join(dir, "fail")
	if err := os.WriteFile(fail, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ISCSI_TEST_LOG", log)
	t.Setenv("ISCSI_TEST_FAIL", fail)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$ISCSI_TEST_LOG"
if [ "$2" = session ]; then echo 'tcp: [1] 127.0.0.2:3260,1 iqn.test:target'; exit 0; fi
if [ "$6" = 127.0.0.1:3260 ] && [ ! -e "$ISCSI_TEST_FAIL" ]; then exit 21; fi
if [ "$6" = 127.0.0.2:3260 ] && [ -e "$ISCSI_TEST_FAIL" ]; then exit 1; fi
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "iscsiadm"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	c := iscsiDiskUnmounter{iscsiDisk: &iscsiDisk{VolName: "volume"}, mounter: mount.NewFakeMounter(nil)}
	target := filepath.Join(dir, "missing")
	if err := detachDisk(c, target, infoPath); err == nil {
		t.Fatal("failed portal logout was ignored")
	}
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	if err := detachDisk(c, target, infoPath); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(commands); strings.Count(got, "-p 127.0.0.1:3260 -u") != 2 || strings.Count(got, "-p 127.0.0.2:3260 -u") != 2 {
		t.Fatalf("unexpected logout attempts: %s", got)
	}
}

func TestUnsharedPortals(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "iscsi-a.json")
	connector := &iscsiLib.Connector{TargetIqn: "iqn.test", TargetPortals: []string{"one:3260", "two:3260"}}
	other := filepath.Join(dir, "iscsi-b.json")
	if err := os.WriteFile(other, []byte(`{"target_iqn":"iqn.test","target_portal":["two:3260"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	portals, shared, err := unsharedPortals(current, connector)
	if err != nil || !shared || !reflect.DeepEqual(portals, []string{"one:3260"}) {
		t.Fatalf("unsharedPortals = %v, %v, %v", portals, shared, err)
	}
	if err := os.WriteFile(other, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := unsharedPortals(current, connector); err == nil {
		t.Fatal("invalid connection record was accepted")
	}
}
