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

package images

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestImages(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Checkout image delivery") }

type command struct {
	name string
	args []string
	env  map[string]string
}
type fakeRunner struct {
	calls []command
	hook  func(context.Context, command) (string, error)
}

func (f *fakeRunner) Run(ctx context.Context, _ string, env map[string]string, name string, args ...string) (string, error) {
	c := command{name, args, env}
	f.calls = append(f.calls, c)
	for i, arg := range args {
		if arg == "--metadata-file" && i+1 < len(args) {
			if err := os.WriteFile(args[i+1], []byte(`{"containerimage.config.digest":"`+testID+`"}`), 0600); err != nil {
				return "", err
			}
		}
	}
	if f.hook != nil {
		return f.hook(ctx, c)
	}
	if name == "docker" && len(args) > 2 && args[0] == "image" {
		return testID, nil
	}
	return "", nil
}

const testID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func contentID(payload string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(payload)))
}
func configManifest(config string) (id, payload string) {
	payload = `{"config":{"digest":"` + config + `"}}`
	return contentID(payload), payload
}

func manager(f Runner) *Manager {
	return &Manager{Runner: f, Options: Options{RepoRoot: "/checkout", Delivery: "kind", KindCluster: "scratch", CurrentContext: "kind-scratch", Arch: "amd64", Timeout: time.Second}}
}
func selectedPod() corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test"}, Spec: corev1.PodSpec{NodeName: "scratch-worker", Containers: []corev1.Container{{Name: "iscsi", Image: "test:build"}}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "iscsi", Ready: true, ImageID: "containerd://" + testID}}}}
}

var _ = Describe("Image contract", func() {
	DescribeTable("requires explicit image tags", func(value string, valid bool) { Expect(ValidateTag(value) == nil).To(Equal(valid)) },
		Entry("local", "iscsi:build", true), Entry("registry port", "127.0.0.1:5000/iscsi:build", true), Entry("empty", "", false), Entry("untagged", "iscsi", false), Entry("latest", "iscsi:latest", false), Entry("canary", "iscsi:canary", false), Entry("digest", "iscsi@"+testID, false), Entry("whitespace", "bad image:tag", false), Entry("URL", "https://registry/iscsi:tag", false))
	DescribeTable("builds this checkout and delivers all three images for the selected architecture", func(arch string) {
		f := &fakeRunner{}
		m := manager(f)
		m.Options.Arch = arch
		set, err := m.Prepare(context.Background(), "driver:build", "target:build", "tools:build")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.calls[0].name).To(Equal("go"))
		Expect(f.calls[0].args).To(ContainElement("./cmd/iscsiplugin"))
		Expect(f.calls[0].env["GOOS"]).To(Equal("linux"))
		Expect(f.calls[0].env["GOARCH"]).To(Equal(arch))
		Expect(f.calls[0].env["CGO_ENABLED"]).To(Equal("0"))
		loaded := []string{}
		files := []string{}
		for _, c := range f.calls {
			if c.name == "kind" {
				loaded = append(loaded, c.args[2])
			}
			if c.name == "docker" && len(c.args) > 1 && c.args[0] == "buildx" && c.args[1] == "build" {
				Expect(c.args).To(ContainElements("--platform", "linux/"+arch, "--build-arg", "ARCH="+arch))
				for i, a := range c.args {
					if a == "-f" {
						files = append(files, c.args[i+1])
					}
				}
			}
		}
		Expect(loaded).To(Equal([]string{"driver:build", "target:build", "tools:build"}))
		Expect(files).To(Equal([]string{"Dockerfile", "deploy/example/tgtd/Dockerfile", "deploy/example/e2e-tools/Dockerfile"}))
		Expect(set.Tools.Policy).To(Equal(corev1.PullNever))
		Expect(set.Driver.LocalID).To(Equal(testID))
	}, Entry("amd64", "amd64"), Entry("arm64", "arm64"))
	It("rejects the entire selection and wrong context before building", func() {
		f := &fakeRunner{}
		m := manager(f)
		_, err := m.Prepare(context.Background(), "driver:build", "target:build", "tools:latest")
		Expect(err).To(HaveOccurred())
		Expect(f.calls).To(BeEmpty())
		m.Options.CurrentContext = "production"
		_, err = m.Prepare(context.Background(), "driver:build", "target:build", "tools:build")
		Expect(err).To(HaveOccurred())
		Expect(f.calls).To(BeEmpty())
	})
	DescribeTable("removes build metadata without hiding the build outcome", func(fail bool) {
		injected := errors.New("build failed")
		f := &fakeRunner{}
		if fail {
			f.hook = func(_ context.Context, c command) (string, error) {
				if c.name == "docker" {
					return "", injected
				}
				return "", nil
			}
		}
		_, err := manager(f).Prepare(context.Background(), "driver:build", "target:build", "tools:build")
		if fail {
			Expect(errors.Is(err, injected)).To(BeTrue())
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
		checked := false
		for _, call := range f.calls {
			for i, arg := range call.args {
				if arg == "--metadata-file" {
					_, statErr := os.Stat(filepath.Dir(call.args[i+1]))
					Expect(os.IsNotExist(statErr)).To(BeTrue())
					checked = true
				}
			}
		}
		Expect(checked).To(BeTrue())
	}, Entry("successful builds", false), Entry("failed build", true))
	DescribeTable("propagates failures without running later operations", func(failure string) {
		f := &fakeRunner{hook: func(_ context.Context, c command) (string, error) {
			if c.name+" "+c.args[0] == failure {
				return "", errors.New("injected")
			}
			if c.name == "docker" && c.args[0] == "image" {
				return testID, nil
			}
			return "", nil
		}}
		_, err := manager(f).Prepare(context.Background(), "driver:build", "target:build", "tools:build")
		Expect(err).To(MatchError(ContainSubstring("injected")))
		Expect(f.calls[len(f.calls)-1].name + " " + f.calls[len(f.calls)-1].args[0]).To(Equal(failure))
	}, Entry("binary", "go build"), Entry("container", "docker buildx"), Entry("load", "kind load"))
	It("selects the matching pushed digest for every registry image", func() {
		f := &fakeRunner{hook: func(_ context.Context, c command) (string, error) {
			if c.name == "docker" && c.args[0] == "buildx" && len(c.args) > 1 && c.args[1] == "imagetools" {
				_, payload := configManifest(testID)
				return payload, nil
			}
			if c.name == "docker" && c.args[0] == "image" {
				if strings.Contains(strings.Join(c.args, " "), "RepoDigests") {
					image := c.args[len(c.args)-1]
					repo := image[:strings.LastIndex(image, ":")]
					return fmt.Sprintf(`["foreign/thing@%s","%s@%s"]`, testID, repo, func() string { id, _ := configManifest(testID); return id }()), nil
				}
				return testID, nil
			}
			return "", nil
		}}
		m := manager(f)
		m.Options.Delivery = "registry"
		set, err := m.Prepare(context.Background(), "r/driver:build", "r/target:build", "r/tools:build")
		Expect(err).NotTo(HaveOccurred())
		id, _ := configManifest(testID)
		Expect(set.Driver.Image).To(Equal("r/driver@" + id))
		Expect(set.Tools.Policy).To(Equal(corev1.PullAlways))
		for _, c := range f.calls {
			Expect(c.name).NotTo(Equal("kind"))
		}
	})
	DescribeTable("refuses missing or malformed build config metadata", func(payload string) {
		f := &fakeRunner{hook: func(_ context.Context, c command) (string, error) {
			for i, arg := range c.args {
				if arg == "--metadata-file" {
					if payload == "remove" {
						Expect(os.Remove(c.args[i+1])).To(Succeed())
					} else {
						Expect(os.WriteFile(c.args[i+1], []byte(payload), 0600)).To(Succeed())
					}
				}
			}
			return "", nil
		}}
		_, err := manager(f).Prepare(context.Background(), "driver:build", "target:build", "tools:build")
		Expect(err).To(HaveOccurred())
		Expect(f.calls).To(HaveLen(2))
	}, Entry("absent", "remove"), Entry("malformed", "{"), Entry("missing config", "{}"), Entry("invalid digest", `{"containerimage.config.digest":"not-a-config"}`))
	It("rejects a pushed immutable config that differs from the actual checkout build", func() {
		wrong, payload := configManifest("sha256:" + strings.Repeat("b", 64))
		f := &fakeRunner{hook: func(_ context.Context, c command) (string, error) {
			if c.name == "docker" && c.args[0] == "image" {
				return `["r/driver@` + wrong + `"]`, nil
			}
			if c.name == "docker" && len(c.args) > 1 && c.args[1] == "imagetools" {
				return payload, nil
			}
			return "", nil
		}}
		m := manager(f)
		m.Options.Delivery = "registry"
		_, err := m.Prepare(context.Background(), "r/driver:build", "r/target:build", "r/tools:build")
		Expect(err).To(MatchError(ContainSubstring("pushed image config differs")))
	})
	DescribeTable("rejects missing or ambiguous repository identities", func(payload string) {
		_, err := RegistryDigest("r/driver:build", payload)
		Expect(err).To(HaveOccurred())
	}, Entry("empty", "[]"), Entry("foreign", `["r/other@`+testID+`"]`), Entry("ambiguous", `["r/driver@`+testID+`","r/driver@`+testID+`"]`), Entry("malformed", "{}"))
	It("checks ready pod image identities and resolves kind manifests", func() {
		p := selectedPod()
		m := manager(&fakeRunner{})
		s := Selection{Image: "test:build", LocalID: testID}
		Expect(m.Verify(context.Background(), []corev1.Pod{p}, "iscsi", s)).To(Succeed())
		p.Status.ContainerStatuses[0].ImageID = "containerd://sha256:" + strings.Repeat("b", 64)
		f := &fakeRunner{hook: func(_ context.Context, c command) (string, error) {
			Expect(c.args[:4]).To(Equal([]string{"exec", "scratch-worker", "crictl", "inspecti"}))
			return `{"status":{"id":"` + testID + `"}}`, nil
		}}
		m.Runner = f
		Expect(m.Verify(context.Background(), []corev1.Pod{p}, "iscsi", s)).To(Succeed())
		Expect(f.calls).To(HaveLen(1))
	})
	DescribeTable("rejects incomplete or changed running pods", func(change string) {
		p := selectedPod()
		switch change {
		case "image":
			p.Spec.Containers[0].Image = "foreign:build"
		case "unready":
			p.Status.ContainerStatuses[0].Ready = false
		case "missing identity":
			p.Status.ContainerStatuses[0].ImageID = ""
		case "node":
			p.Spec.NodeName = ""
		case "container":
			p.Spec.Containers = nil
		case "deleting":
			now := metav1.Now()
			p.DeletionTimestamp = &now
		}
		Expect(manager(&fakeRunner{}).Verify(context.Background(), []corev1.Pod{p}, "iscsi", Selection{Image: "test:build", LocalID: testID})).NotTo(Succeed())
	}, Entry("image", "image"), Entry("unready", "unready"), Entry("identity", "missing identity"), Entry("node", "node"), Entry("container", "container"), Entry("deleting", "deleting"))
	It("uses one enclosing deadline across every runtime fallback", func() {
		deadline := time.Now().Add(time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		f := &fakeRunner{hook: func(ctx context.Context, _ command) (string, error) {
			d, ok := ctx.Deadline()
			Expect(ok).To(BeTrue())
			Expect(d).To(Equal(deadline))
			return "", errors.New("unresolved")
		}}
		m := manager(f)
		m.Options.Timeout = 2 * time.Second
		p := selectedPod()
		p.Status.ContainerStatuses[0].ImageID = "containerd://foreign"
		Expect(m.Verify(ctx, []corev1.Pod{p}, "iscsi", Selection{Image: "test:build", LocalID: testID})).NotTo(Succeed())
		Expect(f.calls).To(HaveLen(2))
	})
	It("never accepts the desired mutable tag as evidence of the running image", func() {
		f := &fakeRunner{hook: func(_ context.Context, c command) (string, error) {
			reference := c.args[len(c.args)-1]
			if reference == "test:build" {
				return `{"status":{"id":"` + testID + `"}}`, nil
			}
			return `{"status":{"id":"sha256:` + strings.Repeat("b", 64) + `"}}`, nil
		}}
		p := selectedPod()
		p.Status.ContainerStatuses[0].ImageID = "containerd://sha256:" + strings.Repeat("b", 64)
		Expect(manager(f).Verify(context.Background(), []corev1.Pod{p}, "iscsi", Selection{Image: "test:build", LocalID: testID})).NotTo(Succeed())
		for _, call := range f.calls {
			Expect(call.args[len(call.args)-1]).NotTo(Equal("test:build"))
		}
	})
	DescribeTable("resolves cached registry index aliases against the exact built config", func(configID string, valid bool) {
		oldID, payload := configManifest(configID)
		newID := "sha256:" + strings.Repeat("c", 64)
		actual := "registry.example/driver@" + oldID
		f := &fakeRunner{hook: func(_ context.Context, c command) (string, error) {
			Expect(c.args[len(c.args)-1]).To(Equal(actual))
			return payload, nil
		}}
		m := manager(f)
		m.Options.Delivery = "registry"
		p := selectedPod()
		p.Spec.Containers[0].Image = "registry.example/driver@" + newID
		p.Status.ContainerStatuses[0].ImageID = actual
		err := m.Verify(context.Background(), []corev1.Pod{p}, "iscsi", Selection{Image: p.Spec.Containers[0].Image, LocalID: testID})
		Expect(err == nil).To(Equal(valid))
		Expect(f.calls).To(HaveLen(1))
	}, Entry("same content with a different index", testID, true), Entry("foreign config under an old index", "sha256:"+strings.Repeat("d", 64), false))
	It("resolves an exact running index and platform manifest under one budget", func() {
		leaf, leafPayload := configManifest(testID)
		index := `{"manifests":[{"digest":"` + leaf + `","platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:` + strings.Repeat("e", 64) + `","platform":{"os":"unknown","architecture":"unknown"}}]}`
		oldID := contentID(index)
		newID := "sha256:" + strings.Repeat("c", 64)
		actual := "registry.example/driver@" + oldID
		deadline := time.Now().Add(time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		f := &fakeRunner{hook: func(ctx context.Context, c command) (string, error) {
			d, _ := ctx.Deadline()
			Expect(d).To(Equal(deadline))
			ref := c.args[len(c.args)-1]
			if ref == actual {
				return index, nil
			}
			Expect(ref).To(Equal("registry.example/driver@" + leaf))
			return leafPayload, nil
		}}
		m := manager(f)
		m.Options.Delivery = "registry"
		m.Options.Timeout = 2 * time.Second
		p := selectedPod()
		p.Spec.Containers[0].Image = "registry.example/driver@" + newID
		p.Status.ContainerStatuses[0].ImageID = actual
		Expect(m.Verify(ctx, []corev1.Pod{p}, "iscsi", Selection{Image: p.Spec.Containers[0].Image, LocalID: testID})).To(Succeed())
		Expect(f.calls).To(HaveLen(2))
	})
	DescribeTable("rejects unresolved immutable platform manifests", func(payload string) {
		m := manager(&fakeRunner{hook: func(_ context.Context, _ command) (string, error) { return payload, nil }})
		_, err := m.registryConfig(context.Background(), "r/driver", contentID(payload), 0)
		Expect(err).To(HaveOccurred())
	}, Entry("malformed", "{"), Entry("missing platform", `{"manifests":[]}`), Entry("invalid config", `{"config":{"digest":"not-a-digest"}}`), Entry("ambiguous platform", `{"manifests":[{"digest":"`+testID+`","platform":{"os":"linux","architecture":"amd64"}},{"digest":"`+testID+`","platform":{"os":"linux","architecture":"amd64"}}]}`))
	It("bounds nested immutable descriptor traversal", func() {
		root, payload := configManifest(testID)
		blobs := map[string]string{root: payload}
		for i := 0; i < 5; i++ {
			payload = `{"manifests":[{"digest":"` + root + `","platform":{"os":"linux","architecture":"amd64"}}]}`
			root = contentID(payload)
			blobs[root] = payload
		}
		m := manager(&fakeRunner{hook: func(_ context.Context, c command) (string, error) {
			_, id, _ := strings.Cut(c.args[len(c.args)-1], "@")
			return blobs[id], nil
		}})
		_, err := m.registryConfig(context.Background(), "r/driver", root, 0)
		Expect(err).To(MatchError(ContainSubstring("excessively nested")))
	})
	It("rejects a registry response that differs from its immutable digest", func() {
		wanted, _ := configManifest(testID)
		_, foreign := configManifest("sha256:" + strings.Repeat("b", 64))
		m := manager(&fakeRunner{hook: func(_ context.Context, _ command) (string, error) { return foreign, nil }})
		_, err := m.registryConfig(context.Background(), "r/driver", wanted, 0)
		Expect(err).To(MatchError(ContainSubstring("raw descriptor digest differs")))
	})
	It("hashes exact registry bytes including trailing whitespace", func() {
		_, payload := configManifest(testID)
		payload += "\n"
		m := manager(&fakeRunner{hook: func(_ context.Context, _ command) (string, error) { return payload, nil }})
		id, err := m.registryConfig(context.Background(), "r/driver", contentID(payload), 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(id).To(Equal(testID))
	})
	It("preserves exact subprocess output bytes for immutable manifest hashing", func() {
		binary, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		output, err := (ProcessRunner{}).Run(context.Background(), "", map[string]string{"IMAGE_HELPER": "raw"}, binary, "-test.run=TestImageProcessHelper")
		Expect(err).NotTo(HaveOccurred())
		Expect(output).To(Equal("raw-content\n"))
	})
	It("rejects empty coverage and expired budgets", func() {
		m := manager(&fakeRunner{})
		Expect(m.Verify(context.Background(), nil, "iscsi", Selection{})).NotTo(Succeed())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		Expect(m.Verify(ctx, []corev1.Pod{selectedPod()}, "iscsi", Selection{Image: "test:build", LocalID: testID})).To(MatchError(context.Canceled))
	})
	It("cancels subprocesses and excludes their raw diagnostics", func() {
		binary, err := os.Executable()
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err = (ProcessRunner{}).Run(ctx, "", map[string]string{"IMAGE_HELPER": "sleep"}, binary, "-test.run=TestImageProcessHelper")
		Expect(err).To(HaveOccurred())
		Expect(ctx.Err()).To(Equal(context.DeadlineExceeded))
		Expect(err.Error()).NotTo(ContainSubstring("secret-sentinel"))
		_, err = (ProcessRunner{}).Run(context.Background(), "", map[string]string{"IMAGE_HELPER": "fail"}, binary, "-test.run=TestImageProcessHelper")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("secret-sentinel"))
	})
})

func TestImageProcessHelper(t *testing.T) {
	switch os.Getenv("IMAGE_HELPER") {
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "raw":
		fmt.Print("raw-content\n")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "secret-sentinel")
		os.Exit(9)
	}
}
