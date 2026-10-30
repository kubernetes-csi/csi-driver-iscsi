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

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/probe"
	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/workload"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

var dialTarget = func(ctx context.Context, budget time.Duration, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: budget}).DialContext(ctx, "tcp", address)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "e2e-tools subcommand required")
		return 2
	}
	mode := args[0]
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "/host", "read-only node root")
	iqn := flags.String("iqn", "", "owned target IQN")
	volume := flags.String("volume", "", "owned volume name")
	tracked := flags.String("tracked", "", "space-separated captured devices")
	lun := flags.String("lun", "", "LUN identifier")
	path := flags.String("path", "", "payload or progress file")
	payload := flags.String("payload", "", "expected payload without final newline")
	directory := flags.String("dir", "/mnt", "mounted data directory")
	progress := flags.String("progress", "/tmp/io-progress", "progress file outside the mounted volume")
	budget := flags.Duration("timeout", 10*time.Second, "operation deadline")
	address := flags.String("address", "", "target host:port")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var err error
	switch mode {
	case "prerequisites":
		err = probe.Prerequisites(*root)
		if err == nil {
			_, err = fmt.Fprintln(stdout, "Node iSCSI prerequisites verified (read-only checks)")
		}
	case "snapshot", "check", "snapshot-lun", "check-lun", "published":
		var devices []string
		devices, err = probe.Volume(mode, probe.VolumeOptions{Root: *root, IQN: *iqn, Volume: *volume, LUN: *lun, Tracked: strings.Fields(*tracked)})
		if err == nil && len(devices) > 0 {
			_, err = fmt.Fprintln(stdout, strings.Join(devices, "\n"))
		}
	case "seed", "verify":
		if *path == "" {
			_, _ = fmt.Fprintln(stderr, "--path is required")
			return 2
		}
		if mode == "seed" {
			err = workload.Seed(*path, *payload)
		} else {
			err = workload.Verify(*path, *payload)
		}
	case "read":
		if *path == "" {
			return 2
		}
		var data []byte
		data, err = os.ReadFile(*path)
		if err == nil {
			_, err = stdout.Write(data)
		}
	case "progress":
		if *path == "" {
			return 2
		}
		var data []byte
		data, err = os.ReadFile(*path)
		if err == nil {
			value := strings.TrimSpace(string(data))
			if _, parseErr := strconv.ParseUint(value, 10, 64); parseErr != nil {
				err = errors.New("invalid shared I/O progress")
			} else {
				_, err = fmt.Fprintln(stdout, value)
			}
		}
	case "hold":
		<-ctx.Done()
	case "io":
		var binary string
		binary, err = os.Executable()
		if err == nil {
			err = workload.RunIO(ctx, *directory, *progress, *budget, time.Second, workload.ProcessOperation(binary))
		}
	case "io-init":
		err = workload.Seed(filepath.Join(*directory, "b-pattern"), workload.SharedPayload)
	case "io-step":
		err = workload.IOStep(*directory)
	case "connect":
		if *address == "" || *budget <= 0 {
			return 2
		}
		var connection net.Conn
		connection, err = dialTarget(ctx, *budget, *address)
		if err == nil {
			err = connection.Close()
		}
	default:
		_, _ = fmt.Fprintln(stderr, "unknown e2e-tools subcommand")
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		var state *probe.Error
		if errors.As(err, &state) {
			return state.Code
		}
		return 1
	}
	return 0
}
