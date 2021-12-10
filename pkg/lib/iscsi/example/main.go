package main

import (
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"github.com/kubernetes-csi/csi-lib-iscsi/iscsi"
)

var (
	portals  = flag.String("portals", "192.168.1.112:3260", "Comma delimited.  Eg: 1.1.1.1,2.2.2.2")
	iqn      = flag.String("iqn", "iqn.2010-10.org.openstack:volume-95739000-1557-44f8-9f40-e9d29fe6ec47", "")
	username = flag.String("username", "3aX7EEf3CEgvESQG75qh", "")
	password = flag.String("password", "eJBDC7Bt7WE3XFDq", "")
	lun      = flag.Int("lun", 1, "")
	debug    = flag.Bool("debug", false, "enable logging")
)

func main() {
	flag.Parse()
	tgtps := strings.Split(*portals, ",")
	if *debug {
		iscsi.EnableDebugLogging(os.Stdout)
	}

	// You can utilize the iscsiadm calls directly if you wish, but by creating a Connector
	// you can simplify interactions to simple calls like "Connect" and "Disconnect"
	c := &iscsi.Connector{
		// Our example uses chap
		AuthType: "chap",
		// List of targets must be >= 1 (>1 signals multipath/mpio)
		TargetIqn:     *iqn,
		TargetPortals: tgtps,
		// CHAP can be setup up for discovery as well as sessions, our example
		// device only uses CHAP security for sessions, for those that use Discovery
		// as well, we'd add a DiscoverySecrets entry the same way
		SessionSecrets: iscsi.Secrets{
			UserName:    *username,
			Password:    *password,
			SecretsType: "chap"},
		// Lun is the lun number the devices uses for exports
		Lun: int32(*lun),
		// Number of times we check for device path, waiting for CheckInterval seconds in between each check (defaults to 10 if omitted)
		RetryCount: 11,
		// CheckInterval is the time in seconds to wait in between device path checks when logging in to a target
		CheckInterval: 1,
	}

	// Now we can just issue a connection request using our Connector
	// A successful connection will include the device path to access our iscsi volume
	path, err := c.Connect()
	if err != nil {
		log.Printf("Error returned from c.Connect: %s", err.Error())
		os.Exit(1)
	}

	log.Printf("Connected device at path: %s\n", path)
	time.Sleep(3 * time.Second)

	// This will disconnect the volume
	if err := c.DisconnectVolume(); err != nil {
		log.Printf("Error returned from c.DisconnectVolume: %s", err.Error())
		os.Exit(1)
	}

	// This will disconnect the session as well as clear out the iscsi DB entries associated with it
	c.Disconnect()
}
