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

// Package images builds this checkout and verifies the images actually running.
package images

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
)

type Runner interface {
	Run(context.Context, string, map[string]string, string, ...string) (string, error)
}

type ProcessRunner struct{}

func (ProcessRunner) Run(ctx context.Context, dir string, env map[string]string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Run(); err != nil {
		// Command output can contain registry credentials. Report the operation
		// and status, leaving raw output out of evidence and console logs.
		return "", fmt.Errorf("%s failed: %w", name, err)
	}
	return out.String(), nil
}

type Options struct {
	RepoRoot, Delivery, KindCluster, CurrentContext, Arch string
	Timeout                                               time.Duration
}

type Selection struct {
	Image, LocalID string
	Policy         corev1.PullPolicy
}

type Set struct{ Driver, Target, Tools Selection }

type Manager struct {
	Options Options
	Runner  Runner
}

var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var tag = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*:[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func ValidateTag(image string) error {
	if !tag.MatchString(image) || strings.Contains(image, "@") || strings.Contains(image, "://") {
		return fmt.Errorf("an explicit image tag is required")
	}
	last := image[strings.LastIndex(image, ":")+1:]
	if last == "latest" || last == "canary" {
		return fmt.Errorf("mutable latest/canary test image tags are not accepted")
	}
	return nil
}

func RegistryDigest(image, payload string) (string, error) {
	var values []string
	if err := json.Unmarshal([]byte(payload), &values); err != nil {
		return "", fmt.Errorf("invalid image repository digests: %w", err)
	}
	repo := image[:strings.LastIndex(image, ":")]
	var matches []string
	for _, value := range values {
		prefix, id, ok := strings.Cut(value, "@")
		if ok && prefix == repo && digest.MatchString(id) {
			matches = append(matches, value)
		}
	}
	if len(matches) != 1 {
		return "", errors.New("cannot resolve a unique pushed image digest")
	}
	return matches[0], nil
}

func (m *Manager) run(ctx context.Context, env map[string]string, name string, args ...string) (string, error) {
	if m.Options.Timeout <= 0 {
		return "", errors.New("positive image command timeout required")
	}
	ctx, cancel := context.WithTimeout(ctx, m.Options.Timeout)
	defer cancel()
	return m.Runner.Run(ctx, m.Options.RepoRoot, env, name, args...)
}

// Prepare validates every selection before building or loading any image.
func (m *Manager) Prepare(ctx context.Context, driver, target, tools string) (result Set, resultErr error) {
	for _, image := range []string{driver, target, tools} {
		if err := ValidateTag(image); err != nil {
			return result, err
		}
	}
	if m.Options.Delivery != "kind" && m.Options.Delivery != "registry" {
		return result, errors.New("delivery must be kind or registry")
	}
	if m.Options.Delivery == "kind" && (m.Options.KindCluster == "" || m.Options.CurrentContext != "kind-"+m.Options.KindCluster) {
		return result, errors.New("current context does not match the selected kind cluster")
	}
	if m.Runner == nil {
		m.Runner = ProcessRunner{}
	}
	if _, err := m.run(ctx, map[string]string{"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": m.Options.Arch}, "go", "build", "-mod=vendor", "-o", filepath.Join("bin", m.Options.Arch, "iscsiplugin"), "./cmd/iscsiplugin"); err != nil {
		return result, err
	}
	metadataDir, err := os.MkdirTemp("", "iscsi-e2e-build-")
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(metadataDir)) }()
	for i, item := range []struct{ image, file string }{{driver, "Dockerfile"}, {target, "deploy/example/tgtd/Dockerfile"}, {tools, "deploy/example/e2e-tools/Dockerfile"}} {
		metadata := filepath.Join(metadataDir, fmt.Sprintf("%d.json", i))
		if _, err := m.run(ctx, nil, "docker", "buildx", "build", "--load", "--metadata-file", metadata, "--platform", "linux/"+m.Options.Arch, "--build-arg", "ARCH="+m.Options.Arch, "-f", item.file, "-t", item.image, "."); err != nil {
			return result, err
		}
		data, err := os.ReadFile(metadata)
		if err != nil {
			return result, err
		}
		var built struct {
			Config string `json:"containerimage.config.digest"`
			Digest string `json:"containerimage.digest"`
		}
		if json.Unmarshal(data, &built) != nil {
			return result, errors.New("invalid built image config ID")
		}
		if built.Config == "" && digest.MatchString(built.Digest) {
			// The containerd image-store exporter can omit config.digest. Save
			// the exact build descriptor, not its mutable tag, and verify the
			// OCI descriptor/config hashes while reading the archive in Go.
			archive := filepath.Join(metadataDir, fmt.Sprintf("%d.tar", i))
			if _, err := m.run(ctx, nil, "docker", "image", "save", "--output", archive, built.Digest); err != nil {
				return result, err
			}
			built.Config, err = archiveConfig(archive, built.Digest, m.Options.Arch)
			removeErr := os.Remove(archive)
			if err != nil {
				return result, err
			}
			if removeErr != nil {
				return result, removeErr
			}
		}
		if !digest.MatchString(built.Config) {
			return result, errors.New("invalid built image config ID")
		}
		selection := Selection{Image: item.image, LocalID: built.Config, Policy: corev1.PullNever}
		if m.Options.Delivery == "kind" {
			if _, err := m.run(ctx, nil, "kind", "load", "docker-image", item.image, "--name", m.Options.KindCluster); err != nil {
				return result, err
			}
		} else {
			if _, err := m.run(ctx, nil, "docker", "push", item.image); err != nil {
				return result, err
			}
			payload, err := m.run(ctx, nil, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", item.image)
			if err != nil {
				return result, err
			}
			selection.Image, err = RegistryDigest(item.image, payload)
			if err != nil {
				return result, err
			}
			repository, pushed, _ := strings.Cut(selection.Image, "@")
			config, err := m.registryConfig(ctx, repository, pushed, 0)
			if err != nil {
				return result, err
			}
			if config != selection.LocalID {
				return result, errors.New("pushed image config differs from checkout build")
			}
			selection.Policy = corev1.PullAlways
		}
		switch i {
		case 0:
			result.Driver = selection
		case 1:
			result.Target = selection
		case 2:
			result.Tools = selection
		}
	}
	return result, nil
}

// Verify uses one budget for all runtime fallbacks, never one fresh deadline
// per attempt. API fetches belong to the caller's same enclosing context.
func (m *Manager) Verify(ctx context.Context, pods []corev1.Pod, container string, selected Selection) error {
	if len(pods) == 0 {
		return errors.New("no image-bearing pods to verify")
	}
	ctx, cancel := context.WithTimeout(ctx, m.Options.Timeout)
	defer cancel()
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" {
			return errors.New("cannot verify a deleting or unscheduled pod")
		}
		found := false
		for _, spec := range pod.Spec.Containers {
			if spec.Name != container {
				continue
			}
			found = true
			if spec.Image != selected.Image {
				return fmt.Errorf("pod %s does not select the built image", pod.Name)
			}
			ready := false
			runtimeID := ""
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == container {
					ready = status.Ready
					runtimeID = status.ImageID
				}
			}
			if !ready || runtimeID == "" {
				return fmt.Errorf("pod %s has no ready image identity", pod.Name)
			}
			_, id, _ := strings.Cut(runtimeID, "://")
			if id == "" {
				id = runtimeID
			}
			if _, suffix, ok := strings.Cut(id, "@"); ok {
				id = suffix
			}
			if m.Options.Delivery == "registry" {
				_, expected, _ := strings.Cut(selected.Image, "@")
				if id != expected && id != selected.LocalID {
					// CRI may report an older immutable index alias for the same
					// cached config. Resolve that actual identity, never the desired
					// tag/index as a substitute for evidence about the running image.
					if err := m.verifyRegistryAlias(ctx, runtimeID, id, selected); err != nil {
						return fmt.Errorf("pod %s does not run the built image: %w", pod.Name, err)
					}
				}
			} else if id != selected.LocalID {
				matched := false
				for _, reference := range []string{id, runtimeID} {
					payload, err := m.Runner.Run(ctx, m.Options.RepoRoot, nil, "docker", "exec", pod.Spec.NodeName, "crictl", "inspecti", reference)
					if err != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						continue
					}
					var data struct {
						Status struct {
							ID string `json:"id"`
						} `json:"status"`
					}
					if json.Unmarshal([]byte(payload), &data) == nil && data.Status.ID == selected.LocalID {
						matched = true
						break
					}
				}
				if !matched {
					return fmt.Errorf("pod %s does not run the selected local image", pod.Name)
				}
			}
		}
		if !found {
			return fmt.Errorf("pod %s has no %s container", pod.Name, container)
		}
	}
	return ctx.Err()
}

func (m *Manager) verifyRegistryAlias(ctx context.Context, runtimeID, id string, selected Selection) error {
	if !digest.MatchString(id) {
		return errors.New("invalid runtime image digest")
	}
	repository, _, ok := strings.Cut(selected.Image, "@")
	if !ok {
		return errors.New("registry selection must be immutable")
	}
	actual := runtimeID
	if _, suffix, ok := strings.Cut(actual, "://"); ok {
		actual = suffix
	}
	if repo, _, ok := strings.Cut(actual, "@"); ok {
		if repo != repository {
			return errors.New("runtime image repository differs")
		}
	} else {
		actual = repository + "@" + id
	}
	actualRepository, actualDigest, _ := strings.Cut(actual, "@")
	configID, err := m.registryConfig(ctx, actualRepository, actualDigest, 0)
	if err != nil {
		return err
	}
	if configID != selected.LocalID {
		return errors.New("runtime immutable image config differs from checkout build")
	}
	return nil
}

// registryConfig follows only immutable descriptors for the requested platform.
// Index attestations can change without changing the actual image config.
func (m *Manager) registryConfig(ctx context.Context, repository, id string, depth int) (string, error) {
	return walkConfig(id, m.Options.Arch, depth, func(id string) ([]byte, error) {
		payload, err := m.run(ctx, nil, "docker", "buildx", "imagetools", "inspect", "--raw", repository+"@"+id)
		if err != nil {
			return nil, err
		}
		if fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(payload))) != id {
			return nil, errors.New("registry raw descriptor digest differs")
		}
		return []byte(payload), nil
	})
}

func walkConfig(id, arch string, depth int, load func(string) ([]byte, error)) (string, error) {
	if depth > 3 || !digest.MatchString(id) {
		return "", errors.New("invalid or excessively nested image descriptor")
	}
	payload, err := load(id)
	if err != nil {
		return "", err
	}
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests []struct {
			Digest   string                            `json:"digest"`
			Platform struct{ OS, Architecture string } `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return "", fmt.Errorf("invalid immutable image manifest: %w", err)
	}
	if manifest.Config.Digest != "" {
		if !digest.MatchString(manifest.Config.Digest) {
			return "", errors.New("invalid image config digest")
		}
		return manifest.Config.Digest, nil
	}
	var matches []string
	for _, descriptor := range manifest.Manifests {
		if descriptor.Platform.OS == "linux" && descriptor.Platform.Architecture == arch {
			matches = append(matches, descriptor.Digest)
		}
	}
	if len(matches) != 1 {
		return "", errors.New("image index has no unique requested platform")
	}
	return walkConfig(matches[0], arch, depth+1, load)
}

func archiveConfig(path, root, arch string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	// The archive is read-only; its bytes have been checked before this close.
	defer func() { _ = file.Close() }()
	reader := tar.NewReader(file)
	blobs := map[string][]byte{}
	total := int64(0)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		id := "sha256:" + strings.TrimPrefix(header.Name, "blobs/sha256/")
		if !strings.HasPrefix(header.Name, "blobs/sha256/") || !digest.MatchString(id) || header.Size > 4<<20 || header.Size < 1 {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return "", err
		}
		if !json.Valid(data) {
			continue
		}
		total += int64(len(data))
		if total > 16<<20 {
			return "", errors.New("image archive metadata exceeds limit")
		}
		if _, exists := blobs[id]; exists {
			return "", errors.New("duplicate image archive descriptor")
		}
		blobs[id] = data
	}
	load := func(id string) ([]byte, error) {
		data, ok := blobs[id]
		if !ok {
			return nil, errors.New("immutable build descriptor missing from image archive")
		}
		if fmt.Sprintf("sha256:%x", sha256.Sum256(data)) != id {
			return nil, errors.New("image archive descriptor digest differs")
		}
		return data, nil
	}
	config, err := walkConfig(root, arch, 0, load)
	if err != nil {
		return "", err
	}
	data, err := load(config)
	if err != nil {
		return "", err
	}
	var platform struct{ OS, Architecture string }
	if json.Unmarshal(data, &platform) != nil || platform.OS != "linux" || platform.Architecture != arch {
		return "", errors.New("image archive config platform differs")
	}
	return config, nil
}
