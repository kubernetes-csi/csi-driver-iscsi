package iscsi

import (
	"fmt"
	"strings"
	"time"
)

// Secrets provides optional iscsi security credentials (CHAP settings)
type Secrets struct {
	// SecretsType is the type of Secrets being utilized (currently we only impleemnent "chap"
	SecretsType string `json:"secretsType,omitempty"`
	// UserName is the configured iscsi user login
	UserName string `json:"userName"`
	// Password is the configured iscsi password
	Password string `json:"password"`
	// UserNameIn provides a specific input login for directional CHAP configurations
	UserNameIn string `json:"userNameIn,omitempty"`
	// PasswordIn provides a specific input password for directional CHAP configurations
	PasswordIn string `json:"passwordIn,omitempty"`
}

func iscsiCmd(args ...string) (string, error) {
	stdout, err := execWithTimeout("iscsiadm", args, time.Second*3)

	debug.Printf("Run iscsiadm command: %s", strings.Join(append([]string{"iscsiadm"}, args...), " "))
	iscsiadmDebug(string(stdout), err)

	return string(stdout), err
}

func iscsiadmDebug(output string, cmdError error) {
	debugOutput := strings.Replace(output, "\n", "\\n", -1)
	debug.Printf("Output of iscsiadm command: {output: %s}", debugOutput)
	if cmdError != nil {
		debug.Printf("Error message returned from iscsiadm command: %s", cmdError.Error())
	}
}

// ListInterfaces returns a list of all iscsi interfaces configured on the node
// along with the raw output in Response.StdOut we add the convenience of
// returning a list of entries found
func ListInterfaces() ([]string, error) {
	debug.Println("Begin ListInterface...")
	out, err := iscsiCmd("-m", "iface", "-o", "show")
	return strings.Split(out, "\n"), err
}

// ShowInterface retrieves the details for the specified iscsi interface
// caller should inspect r.Err and use r.StdOut for interface details
func ShowInterface(iface string) (string, error) {
	debug.Println("Begin ShowInterface...")
	out, err := iscsiCmd("-m", "iface", "-o", "show", "-I", iface)
	return out, err
}

// CreateDBEntry sets up a node entry for the specified tgt in the nodes iscsi nodes db
func CreateDBEntry(tgtIQN, portal, iFace string, discoverySecrets, sessionSecrets Secrets) error {
	debug.Println("Begin CreateDBEntry...")
	baseArgs := []string{"-m", "node", "-T", tgtIQN, "-p", portal}
	_, err := iscsiCmd(append(baseArgs, "-I", iFace, "-o", "new")...)
	if err != nil {
		return err
	}

	if discoverySecrets.SecretsType == "chap" {
		debug.Printf("Setting CHAP Discovery...")
		err := createCHAPEntries(baseArgs, discoverySecrets, true)
		if err != nil {
			return err
		}
	}

	if sessionSecrets.SecretsType == "chap" {
		debug.Printf("Setting CHAP Session...")
		err := createCHAPEntries(baseArgs, sessionSecrets, false)
		if err != nil {
			return err
		}
	}

	return err
}

// Discoverydb discovers the iscsi target
func Discoverydb(tp, iface string, discoverySecrets Secrets, chapDiscovery bool) error {
	debug.Println("Begin Discoverydb...")
	baseArgs := []string{"-m", "discoverydb", "-t", "sendtargets", "-p", tp, "-I", iface}
	out, err := iscsiCmd(append(baseArgs, []string{"-o", "new"}...)...)
	if err != nil {
		return fmt.Errorf("failed to create new entry of target in discoverydb, output: %v, err: %v", out, err)
	}

	if chapDiscovery {
		if err := createCHAPEntries(baseArgs, discoverySecrets, true); err != nil {
			return err
		}
	}

	_, err = iscsiCmd(append(baseArgs, []string{"--discover"}...)...)
	if err != nil {
		// delete the discoverydb record
		iscsiCmd(append(baseArgs, []string{"-o", "delete"}...)...)
		return fmt.Errorf("failed to sendtargets to portal %s, err: %v", tp, err)
	}
	return nil
}

func createCHAPEntries(baseArgs []string, secrets Secrets, discovery bool) error {
	args := []string{}
	debug.Printf("Begin createCHAPEntries (discovery=%t)...", discovery)
	if discovery {
		args = append(baseArgs, []string{
			"-o", "update",
			"-n", "discovery.sendtargets.auth.authmethod", "-v", "CHAP",
			"-n", "discovery.sendtargets.auth.username", "-v", secrets.UserName,
			"-n", "discovery.sendtargets.auth.password", "-v", secrets.Password,
		}...)
		if secrets.UserNameIn != "" {
			args = append(args, []string{"-n", "discovery.sendtargets.auth.username_in", "-v", secrets.UserNameIn}...)
		}
		if secrets.PasswordIn != "" {
			args = append(args, []string{"-n", "discovery.sendtargets.auth.password_in", "-v", secrets.PasswordIn}...)
		}

	} else {

		args = append(baseArgs, []string{
			"-o", "update",
			"-n", "node.session.auth.authmethod", "-v", "CHAP",
			"-n", "node.session.auth.username", "-v", secrets.UserName,
			"-n", "node.session.auth.password", "-v", secrets.Password,
		}...)
		if secrets.UserNameIn != "" {
			args = append(args, []string{"-n", "node.session.auth.username_in", "-v", secrets.UserNameIn}...)
		}
		if secrets.PasswordIn != "" {
			args = append(args, []string{"-n", "node.session.auth.password_in", "-v", secrets.PasswordIn}...)
		}
	}

	_, err := iscsiCmd(args...)
	if err != nil {
		return fmt.Errorf("failed to update discoverydb with CHAP, err: %v", err)
	}

	return nil
}

// GetSessions retrieves a list of current iscsi sessions on the node
func GetSessions() (string, error) {
	debug.Println("Begin GetSessions...")
	out, err := iscsiCmd("-m", "session")
	return out, err
}

// Login performs an iscsi login for the specified target
func Login(tgtIQN, portal string) error {
	debug.Println("Begin Login...")
	baseArgs := []string{"-m", "node", "-T", tgtIQN, "-p", portal}
	if _, err := iscsiCmd(append(baseArgs, []string{"-l"}...)...); err != nil {
		// delete the node record from database
		iscsiCmd(append(baseArgs, []string{"-o", "delete"}...)...)
		return fmt.Errorf("failed to sendtargets to portal %s, err: %v", portal, err)
	}
	return nil
}

// Logout logs out the specified target
func Logout(tgtIQN, portal string) error {
	debug.Println("Begin Logout...")
	args := []string{"-m", "node", "-T", tgtIQN, "-p", portal, "-u"}
	iscsiCmd(args...)
	return nil
}

// DeleteDBEntry deletes the iscsi db entry for the specified target
func DeleteDBEntry(tgtIQN string) error {
	debug.Println("Begin DeleteDBEntry...")
	args := []string{"-m", "node", "-T", tgtIQN, "-o", "delete"}
	iscsiCmd(args...)
	return nil
}

// DeleteIFace delete the iface
func DeleteIFace(iface string) error {
	debug.Println("Begin DeleteIFace...")
	iscsiCmd([]string{"-m", "iface", "-I", iface, "-o", "delete"}...)
	return nil
}
