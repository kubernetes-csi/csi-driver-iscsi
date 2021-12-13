package iscsi

import (
	"os/exec"
	"testing"

	"github.com/prashantv/gostub"
	"github.com/stretchr/testify/assert"
)

const defaultInterface = `
# BEGIN RECORD 2.0-874
iface.iscsi_ifacename = default
iface.net_ifacename = <empty>
iface.ipaddress = <empty>
iface.hwaddress = <empty>
iface.transport_name = tcp
iface.initiatorname = <empty>
iface.state = <empty>
iface.vlan_id = 0
iface.vlan_priority = 0
iface.vlan_state = <empty>
iface.iface_num = 0
iface.mtu = 0
iface.port = 0
iface.bootproto = <empty>
iface.subnet_mask = <empty>
iface.gateway = <empty>
iface.dhcp_alt_client_id_state = <empty>
iface.dhcp_alt_client_id = <empty>
iface.dhcp_dns = <empty>
iface.dhcp_learn_iqn = <empty>
iface.dhcp_req_vendor_id_state = <empty>
iface.dhcp_vendor_id_state = <empty>
iface.dhcp_vendor_id = <empty>
iface.dhcp_slp_da = <empty>
iface.fragmentation = <empty>
iface.gratuitous_arp = <empty>
iface.incoming_forwarding = <empty>
iface.tos_state = <empty>
iface.tos = 0
iface.ttl = 0
iface.delayed_ack = <empty>
iface.tcp_nagle = <empty>
iface.tcp_wsf_state = <empty>
iface.tcp_wsf = 0
iface.tcp_timer_scale = 0
iface.tcp_timestamp = <empty>
iface.redirect = <empty>
iface.def_task_mgmt_timeout = 0
iface.header_digest = <empty>
iface.data_digest = <empty>
iface.immediate_data = <empty>
iface.initial_r2t = <empty>
iface.data_seq_inorder = <empty>
iface.data_pdu_inorder = <empty>
iface.erl = 0
iface.max_receive_data_len = 0
iface.first_burst_len = 0
iface.max_outstanding_r2t = 0
iface.max_burst_len = 0
iface.chap_auth = <empty>
iface.bidi_chap = <empty>
iface.strict_login_compliance = <empty>
iface.discovery_auth = <empty>
iface.discovery_logout = <empty>
# END RECORD
`

func TestDiscovery(t *testing.T) {
	tests := map[string]struct {
		tgtPortal       string
		iface           string
		discoverySecret Secrets
		chapDiscovery   bool
		wantErr         bool
		mockedStdout    string
		mockedCmdError  error
	}{
		"DiscoverySuccess": {
			tgtPortal:      "172.18.0.2:3260",
			iface:          "default",
			chapDiscovery:  false,
			mockedStdout:   "172.18.0.2:3260,1 iqn.2016-09.com.openebs.jiva:store1\n",
			mockedCmdError: nil,
		},

		"ConnectionFailure": {
			tgtPortal:     "172.18.0.2:3262",
			iface:         "default",
			chapDiscovery: false,
			mockedStdout: `iscsiadm: cannot make connection to 172.18.0.2: Connection refused
iscsiadm: cannot make connection to 172.18.0.2: Connection refused
iscsiadm: connection login retries (reopen_max) 5 exceeded
iscsiadm: Could not perform SendTargets discovery: encountered connection failure\n`,
			mockedCmdError: exec.Command("exit", "4").Run(),
			wantErr:        true,
		},

		"ChapEntrySuccess": {
			tgtPortal:     "172.18.0.2:3260",
			iface:         "default",
			chapDiscovery: true,
			discoverySecret: Secrets{
				UserNameIn: "dummyuser",
				PasswordIn: "dummypass",
			},
			mockedStdout:   "172.18.0.2:3260,1 iqn.2016-09.com.openebs.jiva:store1\n",
			mockedCmdError: nil,
		},

		"ChapEntryFailure": {
			tgtPortal: "172.18.0.2:3260",
			iface:     "default",
			discoverySecret: Secrets{
				UserNameIn: "dummyuser",
				PasswordIn: "dummypass",
			},
			chapDiscovery: true,
			mockedStdout: `iscsiadm: Login failed to authenticate with target
iscsiadm: discovery login to 172.18.0.2 rejected: initiator error (02/01), non-retryable, giving up
iscsiadm: Could not perform SendTargets discovery.\n`,
			mockedCmdError: exec.Command("exit", "4").Run(),
			wantErr:        true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			defer gostub.Stub(&execWithTimeout, makeFakeExecWithTimeout(false, []byte(tt.mockedStdout), tt.mockedCmdError)).Reset()
			err := Discoverydb(tt.tgtPortal, tt.iface, tt.discoverySecret, tt.chapDiscovery)
			if (err != nil) != tt.wantErr {
				t.Errorf("Discoverydb() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestCreateDBEntry(t *testing.T) {
	tests := map[string]struct {
		tgtPortal       string
		tgtIQN          string
		iface           string
		discoverySecret Secrets
		sessionSecret   Secrets
		wantErr         bool
		mockedStdout    string
		mockedCmdError  error
	}{
		"CreateDBEntryWithChapDiscoverySuccess": {
			tgtPortal: "192.168.1.107:3260",
			tgtIQN:    "iqn.2010-10.org.openstack:volume-eb393993-73d0-4e39-9ef4-b5841e244ced",
			iface:     "default",
			discoverySecret: Secrets{
				UserNameIn:  "dummyuser",
				PasswordIn:  "dummypass",
				SecretsType: "chap",
			},
			sessionSecret: Secrets{
				UserNameIn:  "dummyuser",
				PasswordIn:  "dummypass",
				SecretsType: "chap",
			},
			mockedStdout:   nodeDB,
			mockedCmdError: nil,
		},
		"CreateDBEntryWithChapDiscoveryFailure": {
			tgtPortal:      "172.18.0.2:3260",
			tgtIQN:         "iqn.2016-09.com.openebs.jiva:store1",
			iface:          "default",
			mockedStdout:   "iscsiadm: No records found\n",
			mockedCmdError: exec.Command("exit", "21").Run(),
			wantErr:        true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			defer gostub.Stub(&execWithTimeout, makeFakeExecWithTimeout(false, []byte(tt.mockedStdout), tt.mockedCmdError)).Reset()
			err := CreateDBEntry(tt.tgtIQN, tt.tgtPortal, tt.iface, tt.discoverySecret, tt.sessionSecret)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateDBEntry() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}

}

func TestListInterfaces(t *testing.T) {
	tests := map[string]struct {
		mockedStdout   string
		mockedCmdError error
		interfaces     []string
		wantErr        bool
	}{
		"EmptyOutput": {
			mockedStdout:   "",
			mockedCmdError: nil,
			interfaces:     []string{""},
			wantErr:        false,
		},
		"DefaultInterface": {
			mockedStdout:   "default",
			mockedCmdError: nil,
			interfaces:     []string{"default"},
			wantErr:        false,
		},
		"TwoInterface": {
			mockedStdout:   "default\ntest",
			mockedCmdError: nil,
			interfaces:     []string{"default", "test"},
			wantErr:        false,
		},
		"HasError": {
			mockedStdout:   "",
			mockedCmdError: exec.Command("exit", "1").Run(),
			interfaces:     []string{},
			wantErr:        true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			defer gostub.Stub(&execWithTimeout, makeFakeExecWithTimeout(false, []byte(tt.mockedStdout), tt.mockedCmdError)).Reset()
			interfaces, err := ListInterfaces()

			if tt.wantErr {
				assert.NotNil(err)
			} else {
				assert.Nil(err)
				assert.Equal(interfaces, tt.interfaces)
			}
		})
	}
}

func TestShowInterface(t *testing.T) {
	tests := map[string]struct {
		mockedStdout   string
		mockedCmdError error
		iFace          string
		wantErr        bool
	}{
		"DefaultInterface": {
			mockedStdout:   defaultInterface,
			mockedCmdError: nil,
			iFace:          defaultInterface,
			wantErr:        false,
		},
		"HasError": {
			mockedStdout:   "",
			mockedCmdError: exec.Command("exit", "1").Run(),
			iFace:          "",
			wantErr:        true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert := assert.New(t)
			defer gostub.Stub(&execWithTimeout, makeFakeExecWithTimeout(false, []byte(tt.mockedStdout), tt.mockedCmdError)).Reset()
			interfaces, err := ShowInterface("default")

			if tt.wantErr {
				assert.NotNil(err)
			} else {
				assert.Nil(err)
				assert.Equal(interfaces, tt.iFace)
			}
		})
	}
}
