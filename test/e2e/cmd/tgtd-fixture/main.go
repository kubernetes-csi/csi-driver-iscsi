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
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/kubernetes-csi/csi-driver-iscsi/test/e2e/targetfixture"
)

func main() { os.Exit(run()) }
func run() int {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: tgtd-fixture serve|ready|restart")
		return 2
	}
	if os.Args[1] == "restart" {
		binary, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := requestRestart(binary, "/proc/1/exe", syscall.Kill); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	c, err := targetfixture.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	switch os.Args[1] {
	case "ready":
		if err := c.Ready(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "serve":
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(signals)
		var status atomic.Int32
		status.Store(143)
		go func() {
			select {
			case sig := <-signals:
				status.Store(int32(128 + int(sig.(syscall.Signal))))
				cancel()
			case <-ctx.Done():
			}
		}()
		code := c.Serve(ctx, os.Stdout, os.Stderr)
		if ctx.Err() != nil {
			return int(status.Load())
		}
		return code
	default:
		fmt.Fprintln(os.Stderr, "unknown target fixture command")
		return 2
	}
}

func requestRestart(binary, pid1 string, kill func(int, syscall.Signal) error) error {
	self, err := os.Stat(binary)
	if err != nil {
		return err
	}
	init, err := os.Stat(pid1)
	if err != nil || !os.SameFile(self, init) {
		return fmt.Errorf("restart requires this fixture supervisor as container PID1")
	}
	return kill(1, syscall.SIGTERM)
}
