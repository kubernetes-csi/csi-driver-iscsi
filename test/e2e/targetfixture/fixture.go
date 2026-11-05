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

// Package targetfixture provides the disposable target's Go supervisor and
// protocol readiness check. It never overwrites an existing backing file.
package targetfixture

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	IQN                                              string
	Paths                                            []string
	SizeBytes                                        int64
	StartupAttempts                                  int
	StartupInterval, ControlTimeout, ShutdownTimeout time.Duration
	// Command names are injectable for regression tests; normal operation uses
	// the target daemon and administration client installed in the image.
	Daemon, Control string
}

func FromEnv() (Config, error) {
	value := func(name, fallback string) string {
		if s := os.Getenv(name); s != "" {
			return s
		}
		return fallback
	}
	number := func(name, fallback string, min, max int) (int, error) {
		raw := value(name, fallback)
		if raw == "" || strings.Trim(raw, "0123456789") != "" {
			return 0, fmt.Errorf("invalid %s", name)
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < min || n > max || (len(raw) > 1 && raw[0] == '0') {
			return 0, fmt.Errorf("invalid %s", name)
		}
		return n, nil
	}
	c := Config{IQN: value("IQN", "iqn.2026-01.com.test:storage"), Paths: []string{value("DISK_PATH", "/var/lib/iscsi/disk0.img")}, Daemon: "tgtd", Control: "tgtadm"}
	count, err := number("LUN_COUNT", "1", 1, 2)
	if err != nil {
		return c, err
	}
	if count == 2 {
		c.Paths = append(c.Paths, value("DISK_PATH_B", "/var/lib/iscsi/disk1.img"))
	}
	size, err := number("DISK_SIZE", "1024", 1, 999999)
	if err != nil {
		return c, err
	}
	c.SizeBytes = int64(size) * 1024 * 1024
	c.StartupAttempts, err = number("TGTD_STARTUP_ATTEMPTS", "10", 1, 999)
	if err != nil {
		return c, err
	}
	interval, err := number("TGTD_STARTUP_INTERVAL", "1", 0, 99)
	if err != nil {
		return c, err
	}
	c.StartupInterval = time.Duration(interval) * time.Second
	timeout, err := number("TGTD_CONTROL_TIMEOUT", "3", 1, 99)
	if err != nil {
		return c, err
	}
	c.ControlTimeout = time.Duration(timeout) * time.Second
	timeout, err = number("TGTD_SHUTDOWN_TIMEOUT", "3", 1, 99)
	if err != nil {
		return c, err
	}
	c.ShutdownTimeout = time.Duration(timeout) * time.Second
	return c, c.Validate()
}

func (c Config) Validate() error {
	if !strings.HasPrefix(c.IQN, "iqn.") || strings.ContainsAny(c.IQN, " \t\r\n\v\f") {
		return errors.New("invalid target IQN")
	}
	if len(c.Paths) < 1 || len(c.Paths) > 2 || c.SizeBytes <= 0 || c.StartupAttempts < 1 || c.StartupInterval < 0 || c.ControlTimeout <= 0 || c.ShutdownTimeout <= 0 {
		return errors.New("invalid target size or startup bounds")
	}
	seen := map[string]bool{}
	for _, path := range c.Paths {
		key := filepath.Clean(path)
		if path == "" || seen[key] {
			return errors.New("LUN backing paths must be nonempty and differ")
		}
		seen[key] = true
	}
	return nil
}

// Initialize only accepts regular non-symlink files of the requested size.
// O_EXCL closes the create race: another process's file is never truncated.
func Initialize(path string, size int64) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("backing path must be a regular non-symlink file")
		}
		if info.Size() != size {
			return errors.New("existing backing file size differs; refusing to overwrite data")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func commandCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exit.ExitCode()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 124
	}
	return 1
}

func (c Config) control(ctx context.Context, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, c.ControlTimeout)
	defer cancel()
	cmd := exec.CommandContext(bounded, c.Control, append([]string{"--lld", "iscsi"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if bounded.Err() != nil {
		return nil, bounded.Err()
	}
	return output, err
}

// InventoryReady requires the requested LUNs on target 1, with the exact IQN,
// online status and backing paths. A LUN on another target cannot satisfy it.
func InventoryReady(inventory, iqn string, paths []string) bool {
	valid := map[int]bool{}
	target, lun, online, backing := false, 0, false, false
	finish := func() {
		if target && lun >= 1 && lun <= len(paths) && online && backing {
			valid[lun] = true
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(inventory))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "Target":
			finish()
			target = len(fields) >= 3 && fields[1] == "1:" && fields[2] == iqn
			lun, online, backing = 0, false, false
		case fields[0] == "LUN:":
			finish()
			lun = 0
			if len(fields) > 1 {
				lun, _ = strconv.Atoi(fields[1])
			}
			online, backing = false, false
		case strings.HasPrefix(line, "Online:"):
			online = strings.TrimSpace(strings.TrimPrefix(line, "Online:")) == "Yes"
		case strings.HasPrefix(line, "Backing store path:"):
			backing = lun >= 1 && lun <= len(paths) && strings.TrimSpace(strings.TrimPrefix(line, "Backing store path:")) == paths[lun-1]
		}
	}
	if scanner.Err() != nil {
		return false
	}
	finish()
	for lun := 1; lun <= len(paths); lun++ {
		if !valid[lun] {
			return false
		}
	}
	return len(paths) > 0
}

func (c Config) Ready(ctx context.Context) error {
	output, err := c.control(ctx, "--op", "show", "--mode", "target")
	if err != nil {
		return err
	}
	if !InventoryReady(string(output), c.IQN, c.Paths) {
		return errors.New("target LUN inventory is not protocol-ready")
	}
	return nil
}

type daemon struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (d *daemon) exited() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}
func (d *daemon) stop(grace time.Duration) {
	if d.exited() {
		return
	}
	_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-d.done:
		return
	case <-time.After(grace):
	}
	_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
	<-d.done // Always reap the owned daemon after KILL.
}

// Serve returns the original setup/daemon failure and reaps the daemon on every
// return path. Cancellation requests a graceful stop and has signal status143.
// Logging is advisory; a closed writer must not replace the original failure status.
func (c Config) Serve(ctx context.Context, out, diagnostics io.Writer) int {
	if err := c.Validate(); err != nil {
		_, _ = fmt.Fprintln(diagnostics, err)
		return 2
	}
	for _, path := range c.Paths {
		if err := Initialize(path, c.SizeBytes); err != nil {
			_, _ = fmt.Fprintln(diagnostics, err)
			return 1
		}
	}
	cmd := exec.Command(c.Daemon, "-f")
	cmd.Stdout, cmd.Stderr = out, diagnostics
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintln(diagnostics, "cannot start target daemon")
		return 1
	}
	d := &daemon{cmd: cmd, done: make(chan struct{})}
	go func() { d.err = cmd.Wait(); close(d.done) }()
	defer d.stop(c.ShutdownTimeout)
	ready := false
	for attempt := 0; attempt < c.StartupAttempts; attempt++ {
		if ctx.Err() != nil {
			return 143
		}
		if d.exited() {
			_, _ = fmt.Fprintln(diagnostics, "tgtd exited before startup")
			return 1
		}
		if _, err := c.control(ctx, "--op", "show", "--mode", "sys"); err == nil {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return 143
		case <-d.done:
			_, _ = fmt.Fprintln(diagnostics, "tgtd exited before startup")
			return 1
		case <-time.After(c.StartupInterval):
		}
	}
	if !ready {
		_, _ = fmt.Fprintln(diagnostics, "timed out waiting for tgtd control readiness")
		return 1
	}
	if d.exited() {
		_, _ = fmt.Fprintln(diagnostics, "tgtd exited before configuration")
		return 1
	}
	controls := [][]string{{"--op", "new", "--mode", "target", "--tid", "1", "-T", c.IQN}}
	for i, path := range c.Paths {
		controls = append(controls, []string{"--op", "new", "--mode", "logicalunit", "--tid", "1", "--lun", strconv.Itoa(i + 1), "-b", path})
	}
	controls = append(controls, []string{"--op", "bind", "--mode", "target", "--tid", "1", "-I", "ALL"})
	for _, args := range controls {
		if _, err := c.control(ctx, args...); err != nil {
			if ctx.Err() != nil {
				return 143
			}
			_, _ = fmt.Fprintln(diagnostics, "target configuration failed")
			return commandCode(err)
		}
	}
	if err := c.Ready(ctx); err != nil {
		if ctx.Err() != nil {
			return 143
		}
		_, _ = fmt.Fprintln(diagnostics, "target readiness failed")
		return commandCode(err)
	}
	if d.exited() {
		_, _ = fmt.Fprintln(diagnostics, "tgtd exited during configuration")
		return 1
	}
	_, _ = fmt.Fprintln(out, "iSCSI target configured and ready")
	select {
	case <-ctx.Done():
		return 143
	case <-d.done:
		return commandCode(d.err)
	}
}
