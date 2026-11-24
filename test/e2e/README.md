# iSCSI end-to-end tests

## Requirements

- The runner needs Go, kind, Docker with buildx, and the iSCSI TCP kernel
  transport available on the host.
- The kind setup runs a test iSCSI daemon in the host network and makes `/sys`
  writable inside the node. Use a disposable host without other iSCSI sessions.

## Run the tests

Create a disposable kind cluster and run the tests:

```sh
make deploy-kind
make
make e2e-test
```

- The cluster defaults to `kind`. Set `E2E_KIND_CLUSTER` for a different name.
- For an existing disposable cluster, use `make e2e-test` with its matching
  kubeconfig. Nodes need open-iscsi, iscsid and udev.
- For registry delivery, set `E2E_DRIVER_DELIVERY=registry` and set
  `E2E_DRIVER_IMAGE`, `E2E_TARGET_IMAGE`, and `E2E_TOOLS_IMAGE` to writable
  image tags in a registry reachable by the cluster, then run `make e2e-test`.

## Select scenarios

- `E2E_SCENARIOS` defaults to `all`. It runs each recovery scenario with a fresh fixture.
- Find scenario names in the `known` map in [config/config.go](config/config.go).
  Entries with a nonempty value are unavailable for the reason given there.
- Set `E2E_SCENARIOS` to the names you want, separated by commas without spaces.
- Run `btrfs-recovery` and `removal-recovery` alone.
- Use a kernel with Btrfs support for end-to-end testing.

### Removal recovery

- Run `make e2e-test E2E_SCENARIOS=removal-recovery` to test a failed state write after device removal.
- This separate scenario bind-mounts the saved record during device removal, so the final rename fails.
- It removes the fault and requires three CSI unpublish retries to succeed. The shared baseline volume must retain its data.

## Cleanup

- Run `make e2e-teardown` to delete the cluster when done.
- Teardown refuses to delete the cluster while any iSCSI session remains.
- Cleanup retains resources if node teardown cannot be verified. Do not remove
  retained resources while a volume may still be mounted. Set
  `E2E_RETAIN_ON_FAILURE=1` to retain resources after other failures.
