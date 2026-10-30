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
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func archiveFixture(mode string) (path, root, config string) {
	GinkgoHelper()
	hash := func(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
	arch := "amd64"
	if mode == "wrong platform" {
		arch = "arm64"
	}
	data := []byte(`{"os":"linux","architecture":"` + arch + `","rootfs":{"type":"layers","diff_ids":[]}}`)
	config = hash(data)
	leaf := []byte(`{"schemaVersion":2,"config":{"digest":"` + config + `"}}`)
	leafID := hash(leaf)
	index := []byte(`{"schemaVersion":2,"manifests":[{"digest":"` + leafID + `","platform":{"os":"linux","architecture":"amd64"}}]}`)
	root = hash(index)
	path = filepath.Join(GinkgoT().TempDir(), "image.tar")
	file, err := os.Create(path)
	Expect(err).NotTo(HaveOccurred())
	writer := tar.NewWriter(file)
	for _, blob := range []struct {
		id   string
		data []byte
	}{{root, index}, {leafID, leaf}, {config, data}} {
		if mode == "missing config" && blob.id == config {
			continue
		}
		if mode == "missing root" && blob.id == root {
			continue
		}
		if mode == "corrupt root" && blob.id == root {
			blob.data = append(blob.data, '\n')
		}
		header := &tar.Header{Name: "blobs/sha256/" + strings.TrimPrefix(blob.id, "sha256:"), Mode: 0600, Size: int64(len(blob.data))}
		Expect(writer.WriteHeader(header)).To(Succeed())
		_, err = writer.Write(blob.data)
		Expect(err).NotTo(HaveOccurred())
		if mode == "duplicate" && blob.id == root {
			Expect(writer.WriteHeader(header)).To(Succeed())
			_, err = writer.Write(blob.data)
			Expect(err).NotTo(HaveOccurred())
		}
	}
	Expect(writer.Close()).To(Succeed())
	Expect(file.Close()).To(Succeed())
	return
}

var _ = Describe("Immutable local image archive", func() {
	It("verifies the exact build index, platform and config hashes", func() {
		path, root, config := archiveFixture("")
		actual, err := archiveConfig(path, root, "amd64")
		Expect(err).NotTo(HaveOccurred())
		Expect(actual).To(Equal(config))
	})
	DescribeTable("rejects incomplete or changed archive evidence", func(mode string) {
		path, root, _ := archiveFixture(mode)
		_, err := archiveConfig(path, root, "amd64")
		Expect(err).To(HaveOccurred())
	}, Entry("changed immutable root", "corrupt root"), Entry("missing config", "missing config"), Entry("missing root", "missing root"), Entry("duplicate descriptor", "duplicate"), Entry("wrong actual config platform", "wrong platform"))
	It("uses the exact build descriptor when exporter metadata omits config.digest", func() {
		path, root, config := archiveFixture("")
		f := &fakeRunner{hook: func(_ context.Context, c command) (string, error) {
			for i, arg := range c.args {
				if arg == "--metadata-file" {
					data, _ := json.Marshal(map[string]string{"containerimage.digest": root})
					Expect(os.WriteFile(c.args[i+1], data, 0600)).To(Succeed())
				}
			}
			if c.name == "docker" && len(c.args) > 1 && c.args[0] == "image" && c.args[1] == "save" {
				Expect(c.args[len(c.args)-1]).To(Equal(root))
				data, err := os.ReadFile(path)
				Expect(err).NotTo(HaveOccurred())
				Expect(os.WriteFile(c.args[3], data, 0600)).To(Succeed())
			}
			return "", nil
		}}
		set, err := manager(f).Prepare(context.Background(), "driver:build", "target:build", "tools:build")
		Expect(err).NotTo(HaveOccurred())
		Expect(set.Driver.LocalID).To(Equal(config))
		Expect(set.Tools.LocalID).To(Equal(config))
	})
})
