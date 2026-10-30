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

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	candidates := []string{dir}
	// Like the original script, a locally built tool can be invoked outside
	// the checkout. With trimmed source paths, invoke it from the checkout.
	if _, source, _, ok := runtime.Caller(0); ok && filepath.IsAbs(source) {
		candidates = append(candidates, filepath.Dir(source))
	}
	for _, dir := range candidates {
		for {
			if _, err := os.Stat(filepath.Join(dir, "deploy/csi-iscsi-node.yaml")); err == nil {
				if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
					return dir, nil
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", errors.New("run from the driver repository")
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *exec.ExitError
	if errors.As(err, &e) {
		if status, ok := e.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return e.ExitCode()
	}
	return 127
}

func capture(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if ctx.Err() != nil {
		return output, ctx.Err()
	}
	return output, err
}
