## Install ISCSI CSI driver on a Kubernetes cluster

- Nodes need `iscsiadm`, `iscsid`, udev, and the iSCSI TCP kernel transport.
- The driver uses the node's iSCSI tools and daemon.
- [install CSI driver master version](./install-csi-driver-master.md)


## iscsiadm lifecycle timeouts

The `iscsiplugin` container accepts independent per-command budgets:

| Flag | Default | Applies to |
| --- | --- | --- |
| `--iscsi-login-timeout` | `150s` | Each target/portal login |
| `--iscsi-logout-timeout` | `30s` | Each target/portal logout |

Durations use Go syntax (for example, `180s` or `3m`). Zero selects the default;
negative values are rejected at startup. Add flags to the **iscsiplugin**
container's `args` (named `iscsi` in the manifest) in `deploy/csi-iscsi-node.yaml`, for example:

```yaml
- --iscsi-login-timeout=180s
- --iscsi-logout-timeout=45s
```

These are runtime settings, not persistent volume connection state. A restarted
plugin uses its current logout budget even for volumes published before the
restart. Other iscsiadm commands (session queries, interface/node configuration,
CHAP updates, discovery and rescans) retain the three-second timeout.

### Sizing the budgets

The previous three-second limit could kill the iscsiadm client while iscsid was
still logging in or logging out. Killing that client does **not** cancel daemon
work. Waiting longer reduces premature interruption and follow-up cleanup while
the daemon is still busy; it does not guarantee completion.

As a baseline, [open-iscsi 2.1.8's sample configuration](https://github.com/open-iscsi/open-iscsi/blob/2.1.8/etc/iscsid.conf)
uses `node.conn[0].timeo.login_timeout = 15` and
`node.session.initial_login_retry_max = 8`, a nominal 120-second initial login
retry budget. The default client budget of 150 seconds adds 30 seconds of
headroom. The sample `node.conn[0].timeo.logout_timeout = 15` gets 15 seconds of
headroom with the 30-second client budget. These are reference defaults, **not**
a universal bound: retry timing, kernel/device cleanup and host configuration
can require more time. Inspect the node's effective open-iscsi node records as
well as `iscsid.conf`; changing the latter need not update existing records.
The deployment's iscsiadm wrapper executes on the host via chroot, so the host's
open-iscsi version and configuration matter, not just the packages in the image.
These flags do not change those daemon settings.

The budgets apply to **each command**, not an entire CSI operation. Multiple
portals are processed sequentially, and discovery, device waits, mounts and
cleanup take additional time. Size the caller's CSI RPC deadline to accommodate
the whole operation, not just one login. NodePublishVolume/NodeUnpublishVolume
are called by kubelet; the supplied deployment does not set their RPC timeout.
Its liveness sidecar's `--probe-timeout=3s` and Kubernetes probe `timeoutSeconds`
are health-check settings, not volume-operation deadlines. Any custom CSI client
or test harness must also have a suitable deadline. The command execution path
still uses its own bounded context rather than the incoming RPC context, so an
RPC timeout does not cancel iscsid work. The existing node-wide operation lock
continues to serialize driver attach/detach processing; increasing these budgets
can delay other volumes, and work may still outlive the budget in iscsid.

A timeout remains an error and retains teardown progress for retry. Exit code 15
still requires an exact target/portal session and a usable LUN before reuse;
logout exit code 21 still requires session verification. This change does not
repair an existing stuck session, explain unexpected iSCSI opcodes, or fix
open-iscsi/kernel errors such as `can not safely destroy session` / `EINVAL`.
It neither restarts iscsid nor performs global logout or destructive recovery.
