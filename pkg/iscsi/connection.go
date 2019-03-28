package iscsi

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	iscsi_lib "github.com/kubernetes-csi/csi-lib-iscsi/iscsi"
)

// parseSecret unmarshalls out the provided json secret and attempts to build an iscsi_lib.Secret
// returns empty secret and nil error if secretParams == ""
func parseSecret(secretParams string) (iscsi_lib.Secrets, error) {
	secret := iscsi_lib.Secrets{}
	if secretParams == "" {
		return secret, nil
	}

	if err := json.Unmarshal([]byte(secretParams), &secret); err != nil {
		return secret, err
	}
	secret.SecretsType = "chap"
	return secret, nil
}

// ensureTargetPort checks if the specified target address includes a port, if it doesn't it appends the default 3260
func ensureTargetPort(p string) string {
	if !strings.Contains(p, ":") {
		p = p + ":3260"
	}
	return p

}

// buildPortalList takes the []byte target portal input from the create request and converts it to a proper []string with iscsi port
func buildPortalList(pList []byte) ([]string, error) {
	var p []string
	portals := []string{}

	if err := json.Unmarshal(pList, &portals); err != nil {
		return nil, err
	}
	for _, portal := range portals {
		p = append(p, ensureTargetPort(string(portal)))
	}
	return p, nil
}

// processChapSettings takes the provides NodePublishVolumeRequest and sets up the necessary CHAP settings, and returns them
func processChapSettings(req *csi.NodePublishVolumeRequest) (iscsi_lib.Secrets, iscsi_lib.Secrets, error) {
	chapDiscovery := false
	// For CHAP secrets we're expecting the form (revisit this):
	//   userName: xxx, password: xxx, userNameIn: xxx, passwordIn: xxx
	// NOTE: parseSecret will check for empy session/discovery secret parameters in the req when processing
	// and return empty secret and nil err
	sessionSecret, err := parseSecret(req.GetVolumeContext()["sessionSecret"])
	if err != nil {
		return iscsi_lib.Secrets{}, iscsi_lib.Secrets{}, err
	}

	discoverySecret, err := parseSecret(req.GetVolumeContext()["discoverySecret"])
	if err != nil {
		return iscsi_lib.Secrets{}, iscsi_lib.Secrets{}, err
	}

	if req.GetVolumeContext()["discoveryCHAPAuth"] == "true" {
		if discoverySecret == (iscsi_lib.Secrets{}) {
			return iscsi_lib.Secrets{}, iscsi_lib.Secrets{}, fmt.Errorf("CHAP discovery was enabled, however no discoverySecret was provided")

		}
		chapDiscovery = true
	}

	// We require that if you enable CHAP it's used for sessions, in other words we don't allow settign it for discovery only
	if req.GetVolumeContext()["sessionCHAPAuth"] == "true" || chapDiscovery {
		if sessionSecret == (iscsi_lib.Secrets{}) {
			return iscsi_lib.Secrets{}, iscsi_lib.Secrets{}, fmt.Errorf("CHAP session was enabled, however no sessionSecret was provided")

		}
	}
	return discoverySecret, sessionSecret, nil
}

// buildISCSIConnector takes a NodePublishVolumeRequest and attempts to build a valid connector from it
func buildISCSIConnector(req *csi.NodePublishVolumeRequest) (*iscsi_lib.Connector, error) {
	tiqn := req.GetVolumeContext()["iqn"]
	lun := req.GetVolumeContext()["lun"]
	portals := req.GetVolumeContext()["portals"]
	pList := strings.Split(portals, ",")
	if len(pList) < 1 || tiqn == "" || lun == "" {
		return nil, fmt.Errorf("unable to create connection, missing required target information: targetPortal, iqn and lun")
	}

	discoverySecret, sessionSecret, err := processChapSettings(req)
	if err != nil {
		return nil, err
	}

	// prelim checks are good, let's parse everything out and build the connector
	c := iscsi_lib.Connector{
		VolumeName:    req.GetVolumeId(),
		TargetIqn:     tiqn,
		TargetPortals: pList,
	}

	if lun != "" {
		l, err := strconv.Atoi(lun)
		if err != nil {
			return nil, err
		}
		c.Lun = int32(l)
	}

	if sessionSecret != (iscsi_lib.Secrets{}) {
		c.SessionSecrets = sessionSecret
		if discoverySecret != (iscsi_lib.Secrets{}) {
			c.DiscoverySecrets = discoverySecret
		}

	}
	if len(portals) > 1 {
		c.Multipath = true
	}
	return &c, nil
}
