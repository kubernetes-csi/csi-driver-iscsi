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

package probe_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/probe"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestProbe(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Read-only node probes") }

func mkdir(path string) { GinkgoHelper(); Expect(os.MkdirAll(path, 0755)).To(Succeed()) }
func write(path, contents string) {
	GinkgoHelper()
	mkdir(filepath.Dir(path))
	Expect(os.WriteFile(path, []byte(contents), 0644)).To(Succeed())
}
func expectCode(err error, code int) {
	GinkgoHelper()
	var state *probe.Error
	Expect(err).To(BeAssignableToTypeOf(state))
	Expect(err.(*probe.Error).Code).To(Equal(code))
}
func files(root string) map[string]string {
	result := map[string]string{}
	Expect(filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = string(data)
		}
		return nil
	})).To(Succeed())
	return result
}

var _ = Describe("Existing node prerequisites", func() {
	var root string
	BeforeEach(func() {
		root = GinkgoT().TempDir()
		write(filepath.Join(root, "usr/sbin/iscsiadm"), "must never execute")
		Expect(os.Chmod(filepath.Join(root, "usr/sbin/iscsiadm"), 0755)).To(Succeed())
		write(filepath.Join(root, "proc/1/comm"), "iscsid\n")
		write(filepath.Join(root, "proc/2/comm"), "systemd-udevd\n")
		mkdir(filepath.Join(root, "sys/class/iscsi_transport/tcp"))
	})
	It("validates prepared Linux nodes without modifying their files", func() {
		before := files(root)
		Expect(probe.PrerequisitesOS(root, "linux")).To(Succeed())
		Expect(files(root)).To(Equal(before))
	})
	DescribeTable("reports the specific missing prerequisite", func(path, reason string) {
		Expect(os.RemoveAll(filepath.Join(root, path))).To(Succeed())
		err := probe.PrerequisitesOS(root, "linux")
		expectCode(err, 1)
		Expect(err.Error()).To(ContainSubstring(reason))
	}, Entry("iscsiadm", "usr/sbin/iscsiadm", "iscsiadm is missing"), Entry("iscsid", "proc/1", "iscsid is not running"), Entry("TCP transport", "sys/class/iscsi_transport/tcp", "TCP kernel transport is unavailable"), Entry("udev", "proc/2", "udev is not running"))
	It("rejects non-Linux nodes", func() {
		err := probe.PrerequisitesOS(root, "darwin")
		expectCode(err, 1)
		Expect(err.Error()).To(ContainSubstring("Linux nodes are required"))
	})
	It("requires the host initiator file to be executable", func() {
		Expect(os.Chmod(filepath.Join(root, "usr/sbin/iscsiadm"), 0644)).To(Succeed())
		expectCode(probe.PrerequisitesOS(root, "linux"), 1)
	})
	It("resolves absolute executable symlinks within the host root", func() {
		mkdir(filepath.Join(root, "sbin"))
		Expect(os.Symlink("/usr/sbin/iscsiadm", filepath.Join(root, "sbin/iscsiadm"))).To(Succeed())
		Expect(probe.PrerequisitesOS(root, "linux")).To(Succeed())
		Expect(os.Remove(filepath.Join(root, "usr/sbin/iscsiadm"))).To(Succeed())
		expectCode(probe.PrerequisitesOS(root, "linux"), 1)
	})
	It("accepts the alternative udev process name", func() {
		write(filepath.Join(root, "proc/2/comm"), "udevd\n")
		Expect(probe.PrerequisitesOS(root, "linux")).To(Succeed())
	})
})

var _ = Describe("Volume teardown observations", func() {
	const iqn = "iqn.2026-01.test:owned"
	const volume = "iscsi-pv-run123"
	var root string
	var options probe.VolumeOptions
	BeforeEach(func() {
		root = GinkgoT().TempDir()
		for _, path := range []string{"proc/1", "proc/self", "sys/class/block", "sys/class/iscsi_session", "dev/disk/by-path"} {
			mkdir(filepath.Join(root, path))
		}
		write(filepath.Join(root, "proc/1/mountinfo"), "")
		write(filepath.Join(root, "proc/self/mountinfo"), "")
		options = probe.VolumeOptions{Root: root, IQN: iqn, Volume: volume}
	})
	session := func(name, target, device, lun string) string {
		path := filepath.Join(root, "sys/class/iscsi_session", name)
		write(filepath.Join(path, "targetname"), target+"\n")
		mkdir(filepath.Join(path, "device/target1:0:0/1:0:0:"+lun+"/block", device))
		mkdir(filepath.Join(root, "sys/class/block", device))
		return path
	}
	check := func(mode string) error { _, err := probe.Volume(mode, options); return err }
	It("captures only owned target devices without modifying observations", func() {
		session("session1", iqn, "sdb", "1")
		session("session2", "iqn.other:target", "sdz", "1")
		before := files(root)
		devices, err := probe.Volume("snapshot", options)
		Expect(err).NotTo(HaveOccurred())
		Expect(devices).To(Equal([]string{"sdb"}))
		Expect(files(root)).To(Equal(before))
	})
	It("observes the node PID1 mount namespace and verifies A was published", func() {
		mount := "1 0 8:0 / /var/lib/kubelet/pods/uid/volumes/kubernetes.io~csi/" + volume + "/mount rw - ext4 /dev/sdb rw\n"
		write(filepath.Join(root, "proc/1/mountinfo"), mount)
		expectCode(check("check"), 1)
		Expect(check("published")).To(Succeed())
		write(filepath.Join(root, "proc/1/mountinfo"), "")
		expectCode(check("published"), 2)
		write(filepath.Join(root, "proc/self/mountinfo"), mount)
		Expect(check("check")).To(Succeed())
	})
	It("rejects owned sessions while leaving unrelated sessions alone", func() {
		session("session2", "iqn.other:target", "sdz", "1")
		Expect(check("check")).To(Succeed())
		session("session1", iqn, "sdb", "1")
		expectCode(check("check"), 1)
		Expect(filepath.Join(root, "sys/class/block/sdz")).To(BeADirectory())
	})
	It("detects broken by-path symlinks even after session removal", func() {
		path := filepath.Join(root, "dev/disk/by-path/ip-127.0.0.1-iscsi-"+iqn+"-lun-1")
		Expect(os.Symlink("/not/a/device", path)).To(Succeed())
		expectCode(check("check"), 1)
		Expect(os.Remove(path)).To(Succeed())
		Expect(check("check")).To(Succeed())
	})
	It("requires captured devices to disappear even after the session identity is gone", func() {
		path := session("session1", iqn, "sdb", "1")
		devices, err := probe.Volume("snapshot", options)
		Expect(err).NotTo(HaveOccurred())
		options.Tracked = devices
		Expect(os.Remove(filepath.Join(path, "targetname"))).To(Succeed())
		expectCode(check("check"), 1)
		Expect(os.Remove(filepath.Join(root, "sys/class/block/sdb"))).To(Succeed())
		Expect(check("check")).To(Succeed())
	})
	It("checks one LUN while retaining the shared session and other LUN", func() {
		path := session("session1", iqn, "sdb", "1")
		mkdir(filepath.Join(path, "device/target1:0:0/1:0:0:2/block/sdc"))
		mkdir(filepath.Join(root, "sys/class/block/sdc"))
		options.LUN = "1"
		devices, err := probe.Volume("snapshot-lun", options)
		Expect(err).NotTo(HaveOccurred())
		Expect(devices).To(Equal([]string{"sdb"}))
		options.LUN = "2"
		devices, err = probe.Volume("snapshot-lun", options)
		Expect(err).NotTo(HaveOccurred())
		Expect(devices).To(Equal([]string{"sdc"}))
		Expect(os.Symlink("/dev/sdc", filepath.Join(root, "dev/disk/by-path/ip-host-iscsi-"+iqn+"-lun-2"))).To(Succeed())
		options.LUN = "1"
		options.Tracked = []string{"sdb"}
		expectCode(check("check-lun"), 1)
		Expect(os.Remove(filepath.Join(root, "sys/class/block/sdb"))).To(Succeed())
		Expect(check("check-lun")).To(Succeed())
		Expect(filepath.Join(path, "targetname")).To(BeAnExistingFile())
		Expect(filepath.Join(root, "sys/class/block/sdc")).To(BeADirectory())
	})
	It("does not hide another publication's mount when devices are removed", func() {
		write(filepath.Join(root, "proc/1/mountinfo"), "1 0 8:1 / /var/lib/kubelet/pods/uid/volumes/kubernetes.io~csi/iscsi-pv-b/mount rw - ext4 /dev/sdc rw\n")
		Expect(check("check")).To(Succeed())
		options.Volume = "iscsi-pv-b"
		expectCode(check("check"), 1)
	})
	DescribeTable("fails closed when state cannot be observed", func(path string) {
		Expect(os.RemoveAll(filepath.Join(root, path))).To(Succeed())
		expectCode(check("check"), 2)
	}, Entry("mount namespace", "proc/1/mountinfo"), Entry("block devices", "sys/class/block"), Entry("sessions", "sys/class/iscsi_session"), Entry("device paths", "dev/disk/by-path"))
	It("rejects malformed mountinfo", func() {
		write(filepath.Join(root, "proc/1/mountinfo"), "not mountinfo\n")
		expectCode(check("check"), 2)
	})
	DescribeTable("rejects invalid identifiers before observing state", func(mode string, update func(*probe.VolumeOptions)) {
		update(&options)
		expectCode(check(mode), 2)
	}, Entry("mode", "unknown", func(*probe.VolumeOptions) {}), Entry("volume", "check", func(o *probe.VolumeOptions) { o.Volume = "../escape" }), Entry("target", "check", func(o *probe.VolumeOptions) { o.IQN = "iqn.*" }), Entry("captured path", "check", func(o *probe.VolumeOptions) { o.Tracked = []string{"../../etc"} }), Entry("globbed capture", "check", func(o *probe.VolumeOptions) { o.Tracked = []string{"*.md"} }), Entry("LUN", "check-lun", func(o *probe.VolumeOptions) { o.LUN = "bad" }))
})
