#!/usr/bin/env python3
# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import ctypes
import glob
import os
import socket

for path in glob.glob("/proc/[0-9]*/comm"):
    try:
        with open(path) as comm:
            if comm.read().strip() == "iscsid":
                raise RuntimeError("Another iscsid is running on the host")
    except (FileNotFoundError, PermissionError, ProcessLookupError):
        pass

# Keep the control socket in the node while iscsid uses the host network.
control = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
control.bind("\0ISCSIADM_ABSTRACT_NAMESPACE")
control.listen(128)
os.dup2(control.fileno(), 3, inheritable=True)
os.set_inheritable(3, True)
netns = os.open("/proc/1/ns/net", os.O_RDONLY)
libc = ctypes.CDLL(None, use_errno=True)
if libc.setns(netns, 0) != 0:
    raise OSError(ctypes.get_errno(), "setns")
os.close(netns)
os.environ["LISTEN_FDS"] = "1"
os.execv("/usr/bin/nsenter", ["nsenter", "-t", os.environ["NODE_PID"],
         "-m", "-p", "--", "/bin/sh", "-c",
         "export LISTEN_PID=$$; exec /sbin/iscsid -f"])
