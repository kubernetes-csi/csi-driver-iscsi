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
	klog.V(2).Infof("Executing command '%v' with args: '%v'.\n", command, args)

	// Create a new context and add a timeout to it
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Create command with context
	cmd := execCommandContext(ctx, command, args...)

	// This time we can simply use Output() to get the result.
	out, err := cmd.Output()

	// We want to check the context error to see if the timeout was executed.
	// The error returned by cmd.Output() will be OS specific based on what
	// happens when a process is killed.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		klog.V(2).Infof("Command '%s' timeout reached.\n", command)
		return nil, ctx.Err()
	}

	if err != nil {
		var ee *exec.ExitError
		if ok := errors.Is(err, ee); ok {
			klog.V(2).Infof("Non-zero exit code: %s\n", err)
			err = fmt.Errorf("%s", ee.Stderr)
		}
	}

	klog.V(2).Infof("Finished executing command.")
	return out, err
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
