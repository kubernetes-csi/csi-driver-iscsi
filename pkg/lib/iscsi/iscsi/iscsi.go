package iscsi

import (
	"encoding/json"
	"errors"
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
	debug              *log.Logger
	execCommand        = exec.Command
	execCommandContext = exec.CommandContext
	execWithTimeout    = ExecWithTimeout
	osStat             = os.Stat
	filepathGlob       = filepath.Glob
	osOpenFile         = os.OpenFile
	sleep              = time.Sleep
)

// iscsiSession contains information about an iSCSI session
type iscsiSession struct {
	Protocol string
	ID       int32
	Portal   string
	IQN      string
	Name     string
}

type deviceInfo []Device

// Device contains information about a device
type Device struct {
	Name      string   `json:"name"`
	Hctl      string   `json:"hctl"`
	Children  []Device `json:"children"`
	Type      string   `json:"type"`
	Transport string   `json:"tran"`
	Size      string   `json:"size,omitempty"`
}

type HCTL struct {
	HBA     int
	Channel int
	Target  int
	LUN     int
}

// Connector provides a struct to hold all the needed parameters to make our iSCSI connection
type Connector struct {
	VolumeName        string   `json:"volume_name"`
	TargetIqn         string   `json:"target_iqn"`
	TargetPortals     []string `json:"target_portal"`
	Lun               int32    `json:"lun"`
	AuthType          string   `json:"auth_type"`
	DiscoverySecrets  Secrets  `json:"discovery_secrets"`
	SessionSecrets    Secrets  `json:"session_secrets"`
	Interface         string   `json:"interface"`
	MountTargetDevice *Device  `json:"mount_target_device"`
	Devices           []Device `json:"devices"`
	RetryCount        uint     `json:"retry_count"`
	CheckInterval     uint     `json:"check_interval"`
	DoDiscovery       bool     `json:"do_discovery"`
	DoCHAPDiscovery   bool     `json:"do_chap_discovery"`
}

func init() {
	// by default, we don't log anything, EnableDebugLogging() can turn on some tracing
	debug = log.New(ioutil.Discard, "", 0)
}

// EnableDebugLogging provides a mechanism to turn on debug logging for this package
// output is written to the provided io.Writer
func EnableDebugLogging(writer io.Writer) {
	debug = log.New(writer, "DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile)
}

// parseSession takes the raw stdout from the `iscsiadm -m session` command and encodes it into an iSCSI session type
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

// sessionExists checks if an iSCSI session exists
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

// extractTransportName returns a transport_name from getCurrentSessions output
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

// getCurrentSessions list current iSCSI sessions
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

// waitForPathToExist wait for a file at a path to exists on disk
func waitForPathToExist(devicePath *string, maxRetries, intervalSeconds uint, deviceTransport string) error {
	if devicePath == nil || *devicePath == "" {
		return fmt.Errorf("unable to check unspecified devicePath")
	}

	for i := uint(0); i <= maxRetries; i++ {
		if i != 0 {
			debug.Printf("Device path %q doesn't exists yet, retrying in %d seconds (%d/%d)", *devicePath, intervalSeconds, i, maxRetries)
			sleep(time.Second * time.Duration(intervalSeconds))
		}

		if err := pathExists(devicePath, deviceTransport); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	return os.ErrNotExist
}

// pathExists checks if a file at a path exists on disk
func pathExists(devicePath *string, deviceTransport string) error {
	if deviceTransport == "tcp" {
		_, err := osStat(*devicePath)
		if err != nil {
			if !os.IsNotExist(err) {
				debug.Printf("Error attempting to stat device: %s", err.Error())
				return err
			}
			debug.Printf("Device not found for: %s", *devicePath)
			return err
		}
	} else {
		fpath, err := filepathGlob(*devicePath)
		if err != nil {
			return err
		}
		if fpath == nil {
			return os.ErrNotExist
		}
		// There might be a case that fpath contains multiple device paths if
		// multiple PCI devices connect to same iscsi target. We handle this
		// case at subsequent logic. Pick up only first path here.
		*devicePath = fpath[0]
	}

	return nil
}

// getMultipathDevice returns a multipath device for the configured targets if it exists
func getMultipathDevice(devices []Device) (*Device, error) {
	var multipathDevice *Device

	for _, device := range devices {
		if len(device.Children) != 1 {
			multipathdNotRunning := ""
			if len(device.Children) == 0 {
				multipathdNotRunning = " (is multipathd running?)"
			}
			return nil, fmt.Errorf("device is not mapped to exactly one multipath device%s: %v", multipathdNotRunning, device.Children)
		}
		if multipathDevice != nil && device.Children[0].Name != multipathDevice.Name {
			return nil, fmt.Errorf("devices don't share a common multipath device: %v", devices)
		}
		multipathDevice = &device.Children[0]
	}

	if multipathDevice == nil {
		return nil, fmt.Errorf("multipath device not found")
	}

	if multipathDevice.Type != "mpath" {
		return nil, fmt.Errorf("device is not of mpath type: %v", multipathDevice)
	}

	return multipathDevice, nil
}

// Connect is for backward-compatibility with c.Connect()
func Connect(c Connector) (string, error) {
	return c.Connect()
}

// Connect attempts to connect a volume to this node using the provided Connector info
func (c *Connector) Connect() (string, error) {
	if c.RetryCount == 0 {
		c.RetryCount = 10
	}
	if c.CheckInterval == 0 {
		c.CheckInterval = 1
	}

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

	var lastErr error
	var devicePaths []string
	for _, target := range c.TargetPortals {
		devicePath, err := c.connectTarget(c.TargetIqn, target, iFace, iscsiTransport)
		if err != nil {
			lastErr = err
		} else {
			debug.Printf("Appending device path: %s", devicePath)
			devicePaths = append(devicePaths, devicePath)
		}
	}

	// GetISCSIDevices returns all devices if no paths are given
	if len(devicePaths) < 1 {
		c.Devices = []Device{}
	} else if c.Devices, err = GetISCSIDevices(devicePaths, true); err != nil {
		return "", err
	}

	if len(c.Devices) < 1 {
		iscsiCmd([]string{"-m", "iface", "-I", iFace, "-o", "delete"}...)
		return "", fmt.Errorf("failed to find device path: %s, last error seen: %v", devicePaths, lastErr)
	}

	mountTargetDevice, err := c.getMountTargetDevice()
	c.MountTargetDevice = mountTargetDevice
	if err != nil {
		debug.Printf("Connect failed: %v", err)
		err := RemoveSCSIDevices(c.Devices...)
		if err != nil {
			return "", err
		}
		c.MountTargetDevice = nil
		c.Devices = []Device{}
		return "", err
	}

	if c.IsMultipathEnabled() {
		if err := c.IsMultipathConsistent(); err != nil {
			return "", fmt.Errorf("multipath is inconsistent: %v", err)
		}
	}

	return c.MountTargetDevice.GetPath(), nil
}

func (c *Connector) connectTarget(targetIqn string, target string, iFace string, iscsiTransport string) (string, error) {
	debug.Printf("Process targetIqn: %s, portal: %s\n", targetIqn, target)
	targetParts := strings.Split(target, ":")
	targetPortal := targetParts[0]
	targetPort := defaultPort
	if len(targetParts) > 1 {
		targetPort = targetParts[1]
	}
	baseArgs := []string{"-m", "node", "-T", targetIqn, "-p", targetPortal}
	// Rescan sessions to discover newly mapped LUNs. Do not specify the interface when rescanning
	// to avoid establishing additional sessions to the same target.
	if _, err := iscsiCmd(append(baseArgs, []string{"-R"}...)...); err != nil {
		debug.Printf("Failed to rescan session, err: %v", err)
		if os.IsTimeout(err) {
			debug.Printf("iscsiadm timeout, logging out")
			cmd := execCommand("iscsiadm", append(baseArgs, []string{"-u"}...)...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("could not logout from target: %s", out)
			}
		}
	}

	// create our devicePath that we'll be looking for based on the transport being used
	// portal with port
	portal := strings.Join([]string{targetPortal, targetPort}, ":")
	devicePath := strings.Join([]string{"/dev/disk/by-path/ip", portal, "iscsi", targetIqn, "lun", fmt.Sprint(c.Lun)}, "-")
	if iscsiTransport != "tcp" {
		devicePath = strings.Join([]string{"/dev/disk/by-path/pci", "*", "ip", portal, "iscsi", targetIqn, "lun", fmt.Sprint(c.Lun)}, "-")
	}

	exists, _ := sessionExists(portal, targetIqn)
	if exists {
		debug.Printf("Session already exists, checking if device path %q exists", devicePath)
		if err := waitForPathToExist(&devicePath, c.RetryCount, c.CheckInterval, iscsiTransport); err != nil {
			return "", err
		}
		return devicePath, nil
	}

	if err := c.discoverTarget(targetIqn, iFace, portal); err != nil {
		return "", err
	}

	// perform the login
	err := Login(targetIqn, portal)
	if err != nil {
		debug.Printf("Failed to login: %v", err)
		return "", err
	}

	debug.Printf("Waiting for device path %q to exist", devicePath)
	if err := waitForPathToExist(&devicePath, c.RetryCount, c.CheckInterval, iscsiTransport); err != nil {
		return "", err
	}

	return devicePath, nil
}

func (c *Connector) discoverTarget(targetIqn string, iFace string, portal string) error {
	if c.DoDiscovery {
		// build discoverydb and discover iscsi target
		if err := Discoverydb(portal, iFace, c.DiscoverySecrets, c.DoCHAPDiscovery); err != nil {
			debug.Printf("Error in discovery of the target: %s\n", err.Error())
			return err
		}
	}

	if c.DoCHAPDiscovery {
		// Make sure we don't log the secrets
		err := CreateDBEntry(targetIqn, portal, iFace, c.DiscoverySecrets, c.SessionSecrets)
		if err != nil {
			debug.Printf("Error creating db entry: %s\n", err.Error())
			return err
		}
	}

	return nil
}

// Disconnect is for backward-compatibility with c.Disconnect()
func Disconnect(targetIqn string, targets []string) {
	for _, target := range targets {
		targetPortal := strings.Split(target, ":")[0]
		err := Logout(targetIqn, targetPortal)
		if err != nil {
			return
		}
	}

	deleted := map[string]bool{}
	if _, ok := deleted[targetIqn]; ok {
		return
	}
	deleted[targetIqn] = true
	err := DeleteDBEntry(targetIqn)
	if err != nil {
		return
	}
}

// Disconnect performs a disconnect operation from an appliance.
// Be sure to disconnect all devices properly before doing this as it can result in data loss.
func (c *Connector) Disconnect() {
	Disconnect(c.TargetIqn, c.TargetPortals)
}

// DisconnectVolume removes a volume from a Linux host.
func (c *Connector) DisconnectVolume() error {
	// Steps to safely remove an iSCSI storage volume from a Linux host are as following:
	// 1. Unmount the disk from a filesystem on the system.
	// 2. Flush the multipath map for the disk we’re removing (if multipath is enabled).
	// 3. Remove the physical disk entities that Linux maintains.
	// 4. Take the storage volume (disk) offline on the storage subsystem.
	// 5. Rescan the iSCSI sessions (after unmapping only).
	//
	// DisconnectVolume focuses on step 2 and 3.
	// Note: make sure the volume is already unmounted before calling this method.

	if c.IsMultipathEnabled() {
		if err := c.IsMultipathConsistent(); err != nil {
			return fmt.Errorf("multipath is inconsistent: %v", err)
		}

		debug.Printf("Removing multipath device in path %s.\n", c.MountTargetDevice.GetPath())
		err := FlushMultipathDevice(c.MountTargetDevice)
		if err != nil {
			return err
		}
		if err := RemoveSCSIDevices(c.Devices...); err != nil {
			return err
		}
	} else {
		devicePath := c.MountTargetDevice.GetPath()
		debug.Printf("Removing normal device in path %s.\n", devicePath)
		if err := RemoveSCSIDevices(*c.MountTargetDevice); err != nil {
			return err
		}
	}

	debug.Printf("Finished disconnecting volume.\n")
	return nil
}

// getMountTargetDevice returns the device to be mounted among the configured devices
func (c *Connector) getMountTargetDevice() (*Device, error) {
	if len(c.Devices) > 1 {
		multipathDevice, err := getMultipathDevice(c.Devices)
		if err != nil {
			debug.Printf("mount target is not a multipath device: %v", err)
			return nil, err
		}
		debug.Printf("mount target is a multipath device")
		return multipathDevice, nil
	}

	if len(c.Devices) == 0 {
		return nil, fmt.Errorf("could not find mount target device: connector does not contain any device")
	}

	return &c.Devices[0], nil
}

// IsMultipathEnabled check if multipath is enabled on devices handled by this connector
func (c *Connector) IsMultipathEnabled() bool {
	return c.MountTargetDevice.Type == "mpath"
}

// GetSCSIDevices get SCSI devices from device paths
// It will returns all SCSI devices if no paths are given
func GetSCSIDevices(devicePaths []string, strict bool) ([]Device, error) {
	debug.Printf("Getting info about SCSI devices %s.\n", devicePaths)

	deviceInfo, err := lsblk(devicePaths, strict)
	if err != nil {
		debug.Printf("An error occurred while looking info about SCSI devices: %v", err)
		return nil, err
	}

	return deviceInfo, nil
}

// GetISCSIDevices get iSCSI devices from device paths
// It will returns all iSCSI devices if no paths are given
func GetISCSIDevices(devicePaths []string, strict bool) (devices []Device, err error) {
	scsiDevices, err := GetSCSIDevices(devicePaths, strict)
	if err != nil {
		return
	}

	for i := range scsiDevices {
		device := &scsiDevices[i]
		if device.Transport == "iscsi" {
			devices = append(devices, *device)
		}
	}

	return
}

// lsblk execute the lsblk commands
func lsblk(devicePaths []string, strict bool) (deviceInfo, error) {
	flags := []string{"-rn", "-o", "NAME,KNAME,PKNAME,HCTL,TYPE,TRAN,SIZE"}
	command := execCommand("lsblk", append(flags, devicePaths...)...)
	debug.Println(command.String())
	out, err := command.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			err = fmt.Errorf("%s, (%w)", strings.Trim(string(ee.Stderr), "\n"), ee)
			if strict || ee.ExitCode() != 64 { // ignore the error if some devices have been found when not strict
				return nil, err
			}
			debug.Printf("Could find only some devices: %v", err)
		} else {
			return nil, err
		}
	}

	var devices []*Device
	devicesMap := make(map[string]*Device)
	pkNames := []string{}

	// Parse devices
	lines := strings.Split(strings.Trim(string(out), "\n"), "\n")
	for _, line := range lines {
		columns := strings.Split(line, " ")
		if len(columns) < 5 {
			return nil, fmt.Errorf("invalid output from lsblk: %s", line)
		}
		device := &Device{
			Name:      columns[0],
			Hctl:      columns[3],
			Type:      columns[4],
			Transport: columns[5],
			Size:      columns[6],
		}
		devices = append(devices, device)
		pkNames = append(pkNames, columns[2])
		devicesMap[columns[1]] = device
	}

	// Reconstruct devices tree
	for i, pkName := range pkNames {
		if pkName == "" {
			continue
		}
		device := devices[i]
		parent, ok := devicesMap[pkName]
		if !ok {
			return nil, fmt.Errorf("invalid output from lsblk: parent device %q not found", pkName)
		}
		if parent.Children == nil {
			parent.Children = []Device{}
		}
		parent.Children = append(devicesMap[pkName].Children, *device)
	}

	// Filter devices to keep only the roots of the tree
	var deviceInfo deviceInfo
	for i, device := range devices {
		if pkNames[i] == "" {
			deviceInfo = append(deviceInfo, *device)
		}
	}

	return deviceInfo, nil
}

// writeInSCSIDeviceFile write into special devices files to change devices state
func writeInSCSIDeviceFile(hctl string, file string, content string) error {
	filename := filepath.Join("/sys/class/scsi_device", hctl, "device", file)
	debug.Printf("Write %q in %q.\n", content, filename)

	f, err := osOpenFile(filename, os.O_TRUNC|os.O_WRONLY, 0o200)
	if err != nil {
		debug.Printf("Error while opening file %v: %v\n", filename, err)
		return err
	}

	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		debug.Printf("Error while writing to file %v: %v", filename, err)
		return err
	}

	return nil
}

// RemoveSCSIDevices removes SCSI device(s) from a Linux host.
func RemoveSCSIDevices(devices ...Device) error {
	debug.Printf("Removing SCSI devices %v.\n", devices)

	var errs []error
	for _, device := range devices {
		debug.Printf("Flush SCSI device %v.\n", device.Name)
		if err := device.Exists(); err == nil {
			out, err := execCommand("blockdev", "--flushbufs", device.GetPath()).CombinedOutput()
			if err != nil {
				debug.Printf("Command 'blockdev --flushbufs %s' did not succeed to flush the device: %v\n", device.GetPath(), err)
				return errors.New(string(out))
			}
		} else if !os.IsNotExist(err) {
			return err
		}

		debug.Printf("Put SCSI device %q offline.\n", device.Name)
		err := device.Shutdown()
		if err != nil {
			if !os.IsNotExist(err) { // Ignore device already removed
				errs = append(errs, err)
			}
			continue
		}

		debug.Printf("Delete SCSI device %q.\n", device.Name)
		err = device.Delete()
		if err != nil {
			if !os.IsNotExist(err) { // Ignore device already removed
				errs = append(errs, err)
			}
			continue
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}
	debug.Println("Finished removing SCSI devices.")
	return nil
}

// PersistConnector is for backward-compatibility with c.Persist()
func PersistConnector(c *Connector, filePath string) error {
	return c.Persist(filePath)
}

// Persist persists the Connector to the specified file (ie /var/lib/pfile/myConnector.json)
func (c *Connector) Persist(filePath string) error {
	// file := path.Join("mnt", c.VolumeName+".json")
	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("error creating iSCSI persistence file %s: %s", filePath, err)
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
		return nil, err
	}
	c := Connector{}
	err = json.Unmarshal([]byte(f), &c)
	if err != nil {
		return nil, err
	}

	devicePaths := []string{}
	for _, device := range c.Devices {
		devicePaths = append(devicePaths, device.GetPath())
	}
	if c.MountTargetDevice == nil {
		return nil, fmt.Errorf("mountTargetDevice in the connector is nil")
	}
	if devices, err := GetSCSIDevices([]string{c.MountTargetDevice.GetPath()}, false); err != nil {
		return nil, err
	} else {
		c.MountTargetDevice = &devices[0]
	}

	if c.Devices, err = GetSCSIDevices(devicePaths, false); err != nil {
		return nil, err
	}

	return &c, nil
}

// IsMultipathConsistent check if the currently used device is using a consistent multipath mapping
func (c *Connector) IsMultipathConsistent() error {
	devices := append([]Device{*c.MountTargetDevice}, c.Devices...)

	referenceLUN := struct {
		LUN  int
		Name string
	}{LUN: -1, Name: ""}
	HBA := map[int]string{}
	referenceDevice := devices[0]
	for _, device := range devices {
		if device.Size != referenceDevice.Size {
			return fmt.Errorf("devices size differ: %s (%s) != %s (%s)", device.Name, device.Size, referenceDevice.Name, referenceDevice.Size)
		}

		if device.Type != "mpath" {
			hctl, err := device.HCTL()
			if err != nil {
				return err
			}
			if referenceLUN.LUN == -1 {
				referenceLUN.LUN = hctl.LUN
				referenceLUN.Name = device.Name
			} else if hctl.LUN != referenceLUN.LUN {
				return fmt.Errorf("devices LUNs differ: %s (%d) != %s (%d)", device.Name, hctl.LUN, referenceLUN.Name, referenceLUN.LUN)
			}

			if name, ok := HBA[hctl.HBA]; !ok {
				HBA[hctl.HBA] = device.Name
			} else {
				return fmt.Errorf("two devices are using the same controller (%d): %s and %s", hctl.HBA, device.Name, name)
			}
		}

		wwid, err := device.WWID()
		if err != nil {
			return fmt.Errorf("could not find WWID for device %s: %v", device.Name, err)
		}
		if wwid != referenceDevice.Name {
			return fmt.Errorf("devices WWIDs differ: %s (wwid:%s) != %s (wwid:%s)", device.Name, wwid, referenceDevice.Name, referenceDevice.Name)
		}
	}

	return nil
}

// Exists check if the device exists at its path and returns an error otherwise
func (d *Device) Exists() error {
	_, err := osStat(d.GetPath())
	return err
}

// GetPath returns the path of a device
func (d *Device) GetPath() string {
	if d.Type == "mpath" {
		return filepath.Join("/dev/mapper", d.Name)
	}

	return filepath.Join("/dev", d.Name)
}

// WWID returns the WWID of a device
func (d *Device) WWID() (string, error) {
	timeout := 1 * time.Second
	out, err := execWithTimeout("scsi_id", []string{"-g", "-u", d.GetPath()}, timeout)
	if err != nil {
		return "", err
	}

	return string(out[:len(out)-1]), nil
}

// HCTL returns the HCTL of a device
func (d *Device) HCTL() (*HCTL, error) {
	var hctl []int

	for _, idstr := range strings.Split(d.Hctl, ":") {
		id, err := strconv.Atoi(idstr)
		if err != nil {
			hctl = []int{}
			break
		}
		hctl = append(hctl, id)
	}

	if len(hctl) != 4 {
		return nil, fmt.Errorf("invalid HCTL (%s) for device %q", d.Hctl, d.Name)
	}

	return &HCTL{
		HBA:     hctl[0],
		Channel: hctl[1],
		Target:  hctl[2],
		LUN:     hctl[3],
	}, nil
}

// WriteDeviceFile write in a device file
func (d *Device) WriteDeviceFile(name string, content string) error {
	return writeInSCSIDeviceFile(d.Hctl, name, content)
}

// Shutdown turn off an SCSI device by writing offline\n in /sys/class/scsi_device/h:c:t:l/device/state
func (d *Device) Shutdown() error {
	return d.WriteDeviceFile("state", "offline\n")
}

// Delete detach an SCSI device by writing 1 in /sys/class/scsi_device/h:c:t:l/device/delete
func (d *Device) Delete() error {
	return d.WriteDeviceFile("delete", "1")
}

// Rescan does a rescan of SCSI device by writing 1 in /sys/class/scsi_device/h:c:t:l/device/rescan
func (d *Device) Rescan() error {
	return d.WriteDeviceFile("rescan", "1")
}
