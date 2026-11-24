/*
Copyright 2024 The Kubernetes Authors.

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

package iscsilib

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	klog "k8s.io/klog/v2"
)

// ExecWithTimeout execute a command with a timeout and returns an error if timeout is exceeded
func ExecWithTimeout(command string, args []string, timeout time.Duration) ([]byte, error) {
	// CHAP updates may echo their values on either stream. Do not log their
	// arguments or expose their output in errors returned to higher-level logs.
	sensitive := hasCHAPArguments(args)
	klog.V(2).Infof("Executing command %q", command)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := execCommandContext(ctx, command, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if sensitive {
		out = nil
		stderr.Reset()
	}
	// Keep stderr available to callers inspecting the original typed exit error.
	var exitErr *exec.ExitError
	if !sensitive && errors.As(err, &exitErr) {
		exitErr.Stderr = bytes.Clone(stderr.Bytes())
	}
	// Preserve context identity even when CommandContext reports a killed process.
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil && strings.TrimSpace(stderr.String()) != "" {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	klog.V(2).Infof("Finished executing command %q", command)
	return out, err
}

// hasCHAPArguments deliberately suppresses the entire update's output rather
// than trying to redact individual values which iscsiadm may quote or escape.
func hasCHAPArguments(args []string) bool {
	for _, arg := range args {
		if strings.Contains(arg, ".auth.") {
			return true
		}
	}
	return false
}

// FlushMultipathDevice flushes a multipath device dm-x with command multipath -f /dev/dm-x
func FlushMultipathDevice(device *Device) error {
	devicePath := device.GetPath()
	klog.V(2).Infof("Flushing multipath device '%v'.\n", devicePath)

	timeout := 5 * time.Second
	_, err := execWithTimeout("multipath", []string{"-f", devicePath}, timeout)
	if err != nil {
		if _, e := osStat(devicePath); os.IsNotExist(e) {
			klog.V(2).Infof("Multipath device %v has been removed.\n", devicePath)
		} else {
			if strings.Contains(err.Error(), "map in use") {
				err = fmt.Errorf("device is probably still in use somewhere else: %v", err)
			}
			klog.V(2).Infof("Command 'multipath -f %v' did not succeed to delete the device: %v\n", devicePath, err)
			return err
		}
	}

	klog.V(2).Infof("Finished flushing multipath device %v.\n", devicePath)
	return nil
}

// ResizeMultipathDevice resize a multipath device based on its underlying devices
func ResizeMultipathDevice(device *Device) error {
	klog.V(2).Infof("Resizing multipath device %s\n", device.GetPath())

	if output, err := execCommand("multipathd", "resize", "map", device.Name).CombinedOutput(); err != nil {
		return fmt.Errorf("could not resize multipath device: %s (%v)", output, err)
	}

	return nil
}
