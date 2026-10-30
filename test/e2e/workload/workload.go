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

// Package workload implements the storage data checks run inside disposable pods.
package workload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const SharedPayload = "iSCSI shared target B payload"

// Seed creates the payload once. Existing data is never overwritten: restarts
// must prove persistence rather than accidentally repairing a lost payload.
func Seed(path, payload string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create seed: %w", err)
	}
	if _, err := file.Write([]byte(payload + "\n")); err != nil {
		_ = file.Close()
		return fmt.Errorf("write seed: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync seed: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close seed: %w", err)
	}
	return nil
}

// Verify checks exact bytes, including the terminating newline.
func Verify(path, payload string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	if !bytes.Equal(data, []byte(payload+"\n")) {
		return errors.New("payload bytes differ")
	}
	return nil
}

// IOStep must run as a separate process under an operation deadline. Every
// completed step includes a checked read, writeback, and checked reread.
func IOStep(directory string) error {
	pattern := filepath.Join(directory, "b-pattern")
	if err := Verify(pattern, SharedPayload); err != nil {
		return err
	}
	path := filepath.Join(directory, "b-io")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write([]byte(SharedPayload + "\n")); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return Verify(path, SharedPayload)
}

// Operation performs the potentially blocking mounted-volume work. The process
// implementation below lets the supervisor enforce deadlines without waiting
// forever for a goroutine blocked in storage I/O.
type Operation func(context.Context, string, string) error

// ProcessOperation runs Go child commands, without a shell or other interpreter.
func ProcessOperation(binary string) Operation {
	return func(ctx context.Context, mode, directory string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cmd := exec.Command(binary, mode, "--dir="+directory)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			// Return immediately even if a kernel storage operation cannot yet
			// complete. No further progress can be published after this error.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return ctx.Err()
		}
	}
}

// RunIO seeds B once, then publishes progress only after successful checked I/O.
// An operation failure ends the worker; eventual reconnection cannot erase it.
func RunIO(ctx context.Context, directory, progress string, timeout, interval time.Duration, operation Operation) error {
	if timeout <= 0 || interval < 0 || operation == nil {
		return errors.New("invalid shared I/O configuration")
	}
	// Scratch workload images have no /tmp. Progress lives outside the mounted
	// storage so creating its directory does not depend on successful target I/O.
	if err := os.MkdirAll(filepath.Dir(progress), 0700); err != nil {
		return fmt.Errorf("create progress directory: %w", err)
	}
	step := func(mode string) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := operation(ctx, mode, directory); err != nil {
			return fmt.Errorf("shared I/O operation failed: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("shared I/O deadline exceeded: %w", err)
		}
		return nil
	}
	if err := step("io-init"); err != nil {
		return err
	}
	for count := uint64(1); ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step("io-step"); err != nil {
			return err
		}
		if err := os.WriteFile(progress+".next", []byte(strconv.FormatUint(count, 10)+"\n"), 0600); err != nil {
			return fmt.Errorf("write progress: %w", err)
		}
		if err := os.Rename(progress+".next", progress); err != nil {
			return fmt.Errorf("publish progress: %w", err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
