# iSCSI end-to-end tests

- The runner needs Go, kind, Docker with buildx, and the iSCSI TCP kernel
  transport available on the host.
- The kind setup runs a test iSCSI daemon in the host network and makes `/sys`
  writable inside the node. Use a disposable host without other iSCSI sessions.
- Run `make e2e-unit` for unprivileged tests or `make e2e-fixture-test` for the
  Docker target check.

Create a disposable kind cluster and run the tests:

```sh
make deploy-kind
make
make e2e-test
```

- The cluster defaults to `kind`; set `E2E_KIND_CLUSTER` consistently for a
  different name. Run `make e2e-teardown` to delete it when done.
- Teardown refuses to delete the cluster while any iSCSI session remains.
- For an existing disposable cluster, use `make e2e-test` with its matching
  kubeconfig. Nodes need open-iscsi, iscsid and udev.
- For registry delivery, set `E2E_DRIVER_DELIVERY=registry` and set
`E2E_DRIVER_IMAGE`, `E2E_TARGET_IMAGE`, and `E2E_TOOLS_IMAGE` to writable
  image tags in a registry reachable by the cluster, then run `make e2e-test`.

- `E2E_SCENARIOS` defaults to `all`. Choose `baseline`, `republish`,
  `driver-restart`, `target-restart`, or `shared-target` to run fewer tests.
  Multiple choices are comma-separated.
- Cleanup retains resources if node teardown cannot be verified. Don't remove
  retained resources while a volume may still be mounted. Set
  `E2E_RETAIN_ON_FAILURE=1` to retain resources after other failures.
