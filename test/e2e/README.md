# iSCSI end-to-end tests

- Use a disposable cluster without an existing iSCSI CSI driver. Nodes need
  open-iscsi, iscsid, udev, and the iSCSI TCP kernel transport.
- The runner needs Go, Docker with buildx, and access to the cluster. Use kind
  or a registry accessible to Docker and the cluster nodes.
- Run `make e2e-unit` for unprivileged tests or `make e2e-fixture-test` for the
  Docker target check.

For kind, use a prepared cluster and its matching kubeconfig context:

```sh
export KUBECONFIG=/path/to/kubeconfig
export E2E_DRIVER_DELIVERY=kind
export E2E_KIND_CLUSTER=iscsi-e2e
E2E_SCENARIOS=all make e2e-test
```

For registry delivery, set `E2E_DRIVER_DELIVERY=registry` and set
`E2E_DRIVER_IMAGE`, `E2E_TARGET_IMAGE`, and `E2E_TOOLS_IMAGE` to writable
image tags in a registry reachable by the cluster.

- `E2E_SCENARIOS` defaults to `baseline`. Choose `republish`,
  `driver-restart`, `target-restart`, or `shared-target`, or use `all` for
  the first three. Multiple choices are comma-separated.
- Cleanup retains resources if node teardown cannot be verified. Don't remove
  retained resources while a volume may still be mounted. Set
  `E2E_RETAIN_ON_FAILURE=1` to retain resources after other failures.
- Native testing has failed the teardown check because a device or session
  remained after unmounting. A passing cluster E2E run is not yet established.
