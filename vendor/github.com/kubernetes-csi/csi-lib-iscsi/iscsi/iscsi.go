package iscsi

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultPort = "3260"

var (
	debug           *log.Logger
	execCommand     = exec.Command
	execWithTimeout = ExecWithTimeout
)

type statFunc func(string) (os.FileInfo, error)
type globFunc func(string) ([]string, error)

type iscsiSession struct {
	Protocol string
	ID       int32
	Portal   string
	IQN      string
	Name     string
}

type TargetInfo struct {
	Iqn    string `json:"iqn"`
	Portal string `json:"portal"`
	Port   string `json:"port"`
}

//Connector provides a struct to hold all of the needed parameters to make our iscsi connection
type Connector struct {
	VolumeName       string       `json:"volume_name"`
	Targets          []TargetInfo `json:"targets"`
	Lun              int32        `json:"lun"`
	AuthType         string       `json:"auth_type"`
	DiscoverySecrets Secrets      `json:"discovery_secrets"`
	SessionSecrets   Secrets      `json:"session_secrets"`
	Interface        string       `json:"interface"`
	Multipath        bool         `json:"multipath"`

	// DevicePath is dm-x for a multipath device, and sdx for a normal device.
	DevicePath string `json:"device_path"`

	RetryCount      int32 `json:"retry_count"`
	CheckInterval   int32 `json:"check_interval"`
	DoDiscovery     bool  `json:"do_discovery"`
	DoCHAPDiscovery bool  `json:"do_chap_discovery"`
	TargetIqn       string `json:"target_iqn"`
	TargetPortals   []string `json:"target_portals"`
}

func init() {
	// by default we don't log anything, EnableDebugLogging() can turn on some tracing
	debug = log.New(ioutil.Discard, "", 0)

}

// EnableDebugLogging provides a mechanism to turn on debug logging for this package
// output is written to the provided io.Writer
func EnableDebugLogging(writer io.Writer) {
	debug = log.New(writer, "DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile)
}

// parseSession takes the raw stdout from the iscsiadm -m session command and encodes it into an iscsi session type
func parseSessions(lines string) []iscsiSession {
	entries := strings.Split(strings.TrimSpace(lines), "\n")
	r := strings.NewReplacer("[", "",
		"]", "")

	var sessions []iscsiSession
	for _, entry := range entries {
		e := strings.Fields(entry)
		if len(e) < 4 {
			continue
		}
		protocol := strings.Split(e[0], ":")[0]
		id := r.Replace(e[1])
		id64, _ := strconv.ParseInt(id, 10, 32)
		portal := strings.Split(e[2], ",")[0]

		s := iscsiSession{
			Protocol: protocol,
			ID:       int32(id64),
			Portal:   portal,
			IQN:      e[3],
			Name:     strings.Split(e[3], ":")[1],
		}
		sessions = append(sessions, s)
	}
	return sessions
}

func sessionExists(tgtPortal, tgtIQN string) (bool, error) {
	sessions, err := getCurrentSessions()
	if err != nil {
		return false, err
	}
	for _, s := range sessions {
		if tgtIQN == s.IQN && tgtPortal == s.Portal {
			return true, nil
		}
	}
	return false, nil
}

func extractTransportName(output string) string {
	res := regexp.MustCompile(`iface.transport_name = (.*)\n`).FindStringSubmatch(output)
	if res == nil {
		return ""
	}
	if res[1] == "" {
		return "tcp"
	}
	return res[1]
}

func getCurrentSessions() ([]iscsiSession, error) {

	out, err := GetSessions()
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok && exitErr.ProcessState.Sys().(syscall.WaitStatus).ExitStatus() == 21 {
			return []iscsiSession{}, nil
		}
		return nil, err
	}
	sessions := parseSessions(out)
	return sessions, err
}

func waitForPathToExist(devicePath *string, maxRetries, intervalSeconds int, deviceTransport string) (bool, error) {
	return waitForPathToExistImpl(devicePath, maxRetries, intervalSeconds, deviceTransport, os.Stat, filepath.Glob)
}

func waitForPathToExistImpl(devicePath *string, maxRetries, intervalSeconds int, deviceTransport string, osStat statFunc, filepathGlob globFunc) (bool, error) {
	if devicePath == nil {
		return false, fmt.Errorf("unable to check unspecified devicePath")
	}

	var err error
	for i := 0; i < maxRetries; i++ {
		err = nil
		if deviceTransport == "tcp" {
			_, err = osStat(*devicePath)
			if err != nil && !os.IsNotExist(err) {
				debug.Printf("Error attempting to stat device: %s", err.Error())
				return false, err
			} else if err != nil {
				debug.Printf("Device not found for: %s", *devicePath)
			}

		} else {
			fpath, _ := filepathGlob(*devicePath)
			if fpath == nil {
				err = os.ErrNotExist
			} else {
				// There might be a case that fpath contains multiple device paths if
				// multiple PCI devices connect to same iscsi target. We handle this
				// case at subsequent logic. Pick up only first path here.
				*devicePath = fpath[0]
			}
		}
		if err == nil {
			return true, nil
		}
		if i == maxRetries-1 {
			break
		}
		time.Sleep(time.Second * time.Duration(intervalSeconds))
	}
	return false, err
}

func getMultipathDisk(path string) (string, error) {
	// Follow link to destination directory
	debug.Printf("Checking for multipath device for path: %s", path)
	devicePath, err := os.Readlink(path)
	if err != nil {
		debug.Printf("Failed reading link for multipath disk: %s -- error: %s\n", path, err.Error())
		return "", err
	}
	sdevice := filepath.Base(devicePath)
	// If destination directory is already identified as a multipath device,
	// just return its path
	if strings.HasPrefix(sdevice, "dm-") {
		debug.Printf("Already found multipath device: %s", sdevice)
		return path, nil
	}
	// Fallback to iterating through all the entries under /sys/block/dm-* and
	// check to see if any have an entry under /sys/block/dm-*/slaves matching
	// the device the symlink was pointing at
	dmPaths, err := filepath.Glob("/sys/block/dm-*")
	if err != nil {
		debug.Printf("Glob error: %s", err)
		return "", err
	}
	for _, dmPath := range dmPaths {
		sdevices, err := filepath.Glob(filepath.Join(dmPath, "slaves", "*"))
		if err != nil {
			debug.Printf("Glob error: %s", err)
		}
		for _, spath := range sdevices {
			s := filepath.Base(spath)
			debug.Printf("Basepath: %s", s)
			if sdevice == s {
				// We've found a matching entry, return the path for the
				// dm-* device it was found under
				p := filepath.Join("/dev", filepath.Base(dmPath))
				debug.Printf("Found matching multipath device: %s under dm-* device path %s", sdevice, dmPath)
				return p, nil
			}
		}
	}
	debug.Printf("Couldn't find dm-* path for path: %s, found non dm-* path: %s", path, devicePath)
	return "", fmt.Errorf("couldn't find dm-* path for path: %s, found non dm-* path: %s", path, devicePath)
}

// Connect attempts to connect a volume to this node using the provided Connector info
func Connect(c Connector) (string, error) {
	var lastErr error
	if c.RetryCount == 0 {
		c.RetryCount = 10
	}
	if c.CheckInterval == 0 {
		c.CheckInterval = 1
	}

	if c.RetryCount < 0 || c.CheckInterval < 0 {
		return "", fmt.Errorf("invalid RetryCount and CheckInterval combination, both must be positive integers. "+
			"RetryCount: %d, CheckInterval: %d", c.RetryCount, c.CheckInterval)
	}
	var devicePaths []string
	iFace := "default"
	if c.Interface != "" {
		iFace = c.Interface
	}

	// make sure our iface exists and extract the transport type
	out, err := ShowInterface(iFace)
	if err != nil {
		return "", err
	}
	iscsiTransport := extractTransportName(out)

	for _, target := range c.Targets {
		debug.Printf("process targetIqn: %s, portal: %s\n", target.Iqn, target.Portal)
		baseArgs := []string{"-m", "node", "-T", target.Iqn, "-p", target.Portal}
		// Rescan sessions to discover newly mapped LUNs. Do not specify the interface when rescanning
		// to avoid establishing additional sessions to the same target.
		if _, err := iscsiCmd(append(baseArgs, []string{"-R"}...)...); err != nil {
			debug.Printf("failed to rescan session, err: %v", err)
		}

		// create our devicePath that we'll be looking for based on the transport being used
		port := defaultPort
		if target.Port != "" {
			port = target.Port
		}
		// portal with port
		p := strings.Join([]string{target.Portal, port}, ":")
		devicePath := strings.Join([]string{"/dev/disk/by-path/ip", p, "iscsi", target.Iqn, "lun", fmt.Sprint(c.Lun)}, "-")
		if iscsiTransport != "tcp" {
			devicePath = strings.Join([]string{"/dev/disk/by-path/pci", "*", "ip", p, "iscsi", target.Iqn, "lun", fmt.Sprint(c.Lun)}, "-")
		}

		exists, _ := sessionExists(p, target.Iqn)
		if exists {
			if exists, err := waitForPathToExist(&devicePath, 1, 1, iscsiTransport); exists {
				debug.Printf("Appending device path: %s", devicePath)
				devicePaths = append(devicePaths, devicePath)
				continue
			} else if err != nil {
				return "", err
			}
		}

		if c.DoDiscovery {
			// build discoverydb and discover iscsi target
			if err := Discoverydb(p, iFace, c.DiscoverySecrets, c.DoCHAPDiscovery); err != nil {
				debug.Printf("Error in discovery of the target: %s\n", err.Error())
				lastErr = err
				continue
			}
		}

		if c.DoCHAPDiscovery {
			// Make sure we don't log the secrets
			err := CreateDBEntry(target.Iqn, p, iFace, c.DiscoverySecrets, c.SessionSecrets)
			if err != nil {
				debug.Printf("Error creating db entry: %s\n", err.Error())
				continue
			}
		}

		// perform the login
		err = Login(target.Iqn, p)
		if err != nil {
			debug.Printf("failed to login, err: %v", err)
			lastErr = err
			continue
		}
		retries := int(c.RetryCount / c.CheckInterval)
		if exists, err := waitForPathToExist(&devicePath, retries, int(c.CheckInterval), iscsiTransport); exists {
			devicePaths = append(devicePaths, devicePath)
			continue
		} else if err != nil {
			lastErr = fmt.Errorf("couldn't attach disk, err: %v", err)
		}
	}

	if len(devicePaths) < 1 {
		iscsiCmd([]string{"-m", "iface", "-I", iFace, "-o", "delete"}...)
		return "", fmt.Errorf("failed to find device path: %s, last error seen: %v", devicePaths, lastErr)
	}

	if lastErr != nil {
		debug.Printf("Last error occured during iscsi init: \n%v", lastErr)
	}

	for i, path := range devicePaths {
		if path != "" {
			if mappedDevicePath, err := getMultipathDisk(path); mappedDevicePath != "" {
				devicePaths[i] = mappedDevicePath
				if err != nil {
					return "", err
				}
			}
		}
	}
	debug.Printf("After connect we're returning devicePaths: %s", devicePaths)
	if len(devicePaths) > 0 {
		return devicePaths[0], err

	}
	return "", err
}

//Disconnect performs a disconnect operation on a volume
func Disconnect(tgtIqn string, portals []string) error {
	err := Logout(tgtIqn, portals)
	if err != nil {
		return err
	}
	err = DeleteDBEntry(tgtIqn)
	return err
}

// DisconnectVolume removes a volume from a Linux host.
func DisconnectVolume(c Connector) error {
	// Steps to safely remove an iSCSI storage volume from a Linux host are as following:
	// 1. Unmount the disk from a filesystem on the system.
	// 2. Flush the multipath map for the disk we’re removing (if multipath is enabled).
	// 3. Remove the physical disk entities that Linux maintains.
	// 4. Take the storage volume (disk) offline on the storage subsystem.
	// 5. Rescan the iSCSI sessions.
	//
	// DisconnectVolume focuses on step 2 and 3.
	// Note: make sure the volume is already unmounted before calling this method.

	debug.Printf("Disconnecting volume in path %s.\n", c.DevicePath)
	if c.Multipath {
		debug.Printf("Removing multipath device.\n")
		devices, err := GetSysDevicesFromMultipathDevice(c.DevicePath)
		if err != nil {
			return err
		}
		err = FlushMultipathDevice(c.DevicePath)
		if err != nil {
			return err
		}
		debug.Printf("Found multipath slaves %v, removing all of them.\n", devices)
		if err := RemovePhysicalDevice(devices...); err != nil {
			return err
		}
	} else {
		debug.Printf("Removing normal device.\n")
		if err := RemovePhysicalDevice(c.DevicePath); err != nil {
			return err
		}
	}

	debug.Printf("Finished disconnecting volume.\n")
	return nil
}

// RemovePhysicalDevice removes device(s) sdx from a Linux host.
func RemovePhysicalDevice(devices ...string) error {
	debug.Printf("Removing scsi device %v.\n", devices)
	var errs []error
	for _, deviceName := range devices {
		if deviceName == "" {
			continue
		}

		debug.Printf("Delete scsi device %v.\n", deviceName)
		// Remove a scsi device by executing 'echo "1" > /sys/block/sdx/device/delete
		filename := filepath.Join(sysBlockPath, deviceName, "device", "delete")
		if f, err := os.OpenFile(filename, os.O_TRUNC|os.O_WRONLY, 0200); err != nil {
			if os.IsNotExist(err) {
				continue
			} else {
				debug.Printf("Error while opening file %v: %v\n", filename, err)
				errs = append(errs, err)
				continue
			}
		} else {
			defer f.Close()
			if _, err := f.WriteString("1"); err != nil {
				debug.Printf("Error while writing to file %v: %v", filename, err)
				errs = append(errs, err)
				continue
			}
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}
	debug.Println("Finshed removing SCSI devices.")
	return nil
}

// PersistConnector persists the provided Connector to the specified file (ie /var/lib/pfile/myConnector.json)
func PersistConnector(c *Connector, filePath string) error {
	//file := path.Join("mnt", c.VolumeName+".json")
	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error creating iscsi persistence file %s: %s", filePath, err)
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	if err = encoder.Encode(c); err != nil {
		return fmt.Errorf("error encoding connector: %v", err)
	}
	return nil

}

// GetConnectorFromFile attempts to create a Connector using the specified json file (ie /var/lib/pfile/myConnector.json)
func GetConnectorFromFile(filePath string) (*Connector, error) {
	f, err := ioutil.ReadFile(filePath)
	if err != nil {
		return &Connector{}, err

	}
	data := Connector{}
	err = json.Unmarshal(f, &data)
	if err != nil {
		return &Connector{}, err
	}

	return &data, nil

}
