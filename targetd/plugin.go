// +build targetd

package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	"github.com/kubernetes-csi/csi-driver-iscsi/pkg/iscsi"
	"github.com/powerman/rpc-codec/jsonrpc2"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func CreateVolume(cs *iscsi.ControllerServer, ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	glog.Infof("plugin.CreateVolume called")

	v, err := genVolInfoFromCreateVolumeRequest(req)
	if err != nil {
		glog.Warningf("Generating volInfo from %v failed: %v", req, err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	cl := NewtargetdClient(genTargetdURL(*v))
	err = cl.volCreate(v.volID, v.size, v.pool)
	if err != nil {
		glog.Warningf("Failed to create volume %v: %v", v, err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	volumeContext := req.GetParameters()
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      v.volID,
			CapacityBytes: v.size,
			VolumeContext: volumeContext,
		},
	}, nil
}

func DeleteVolume(cs *iscsi.ControllerServer, ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	glog.Infof("plugin.DeleteVolume called")

	v, err := genVolInfoFromDeleteVolumeRequest(req)
	if err != nil {
		glog.Warningf("Generating volInfo from %v failed: %v", req, err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	cl := NewtargetdClient(genTargetdURL(*v))
	err = cl.volDestroy(v.volID, v.pool)
	if err != nil {
		glog.Warningf("Failed to delete volume %v: %v", v, err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.DeleteVolumeResponse{}, nil
}

func ValidateVolumeCapabilities(cs *iscsi.ControllerServer, ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Empty volume ID in request")
	}

	if len(req.VolumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Empty volume capabilities in request")
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.VolumeCapabilities,
		},
	}, nil
}

type volInfo struct {
	volID string
	name  string
	size  int64

	scheme   string
	address  string
	port     string
	pool     string
	username string
	password string
}

func genVolInfoFromCreateVolumeRequest(req *csi.CreateVolumeRequest) (*volInfo, error) {
	v := &volInfo{}

	if v.name = req.GetName(); v.name == "" {
		return nil, fmt.Errorf("missing name in parameters")
	}
	if req.GetCapacityRange() != nil {
		v.size = req.GetCapacityRange().GetRequiredBytes()
	} else {
		return nil, fmt.Errorf("missing volume size in parameters")
	}

	var ok bool
	if v.scheme, ok = req.GetParameters()["targetd-scheme"]; !ok {
		return nil, fmt.Errorf("missing targetd-scheme in parameters")
	}
	if v.address, ok = req.GetParameters()["targetd-address"]; !ok {
		return nil, fmt.Errorf("missing targetd-address in parameters")
	}
	if v.port, ok = req.GetParameters()["targetd-port"]; !ok {
		return nil, fmt.Errorf("missing targetd-port in parameters")
	}
	if v.pool, ok = req.GetParameters()["targetd-pool"]; !ok {
		return nil, fmt.Errorf("missing targetd-pool in parameters")
	}
	if v.username, ok = req.GetSecrets()["targetd-username"]; !ok {
		return nil, fmt.Errorf("missing targetd-username in secrets")
	}
	if v.password, ok = req.GetSecrets()["targetd-password"]; !ok {
		return nil, fmt.Errorf("missing targetd-password in secrets")
	}

	var err error
	if v.volID, err = encodeVolID(*v); err != nil {
		return nil, fmt.Errorf("encoding volID for volInfo %v failed: %v", v, err)
	}

	return v, nil
}

func genVolInfoFromDeleteVolumeRequest(req *csi.DeleteVolumeRequest) (*volInfo, error) {
	volID := req.GetVolumeId()
	if volID == "" {
		return nil, fmt.Errorf("Empty volume ID in request")
	}
	v, err := decodeVolID(volID)
	if err != nil {
		return nil, fmt.Errorf("decodeVolID for volumeID %s failed: %v", volID, err)
	}
	v.volID = volID

	var ok bool
	if v.username, ok = req.GetSecrets()["targetd-username"]; !ok {
		return nil, fmt.Errorf("missing targetd-username in secrets")
	}
	if v.password, ok = req.GetSecrets()["targetd-password"]; !ok {
		return nil, fmt.Errorf("missing targetd-password in secrets")
	}

	return v, nil
}

func encodeVolID(v volInfo) (string, error) {
	if len(v.scheme) == 0 {
		return "", fmt.Errorf("Scheme information in VolumeInfo shouldn't be empty: %v", v)
	}

	if len(v.address) == 0 {
		return "", fmt.Errorf("Address information in VolumeInfo shouldn't be empty: %v", v)
	}

	if len(v.port) == 0 {
		return "", fmt.Errorf("Port information in VolumeInfo shouldn't be empty: %v", v)
	}

	if len(v.pool) == 0 {
		return "", fmt.Errorf("Pool information in VolumeInfo shouldn't be empty: %v", v)
	}

	if len(v.name) == 0 {
		return "", fmt.Errorf("Name information in VolumeInfo shouldn't be empty: %v", v)
	}

	encScheme := strings.ReplaceAll(base64.RawStdEncoding.EncodeToString([]byte(v.scheme)), "/", "-")
	encAddress := strings.ReplaceAll(base64.RawStdEncoding.EncodeToString([]byte(v.address)), "/", "-")
	encPort := strings.ReplaceAll(base64.RawStdEncoding.EncodeToString([]byte(v.port)), "/", "-")
	encPool := strings.ReplaceAll(base64.RawStdEncoding.EncodeToString([]byte(v.pool)), "/", "-")
	encName := strings.ReplaceAll(base64.RawStdEncoding.EncodeToString([]byte(v.name)), "/", "-")
	return strings.Join([]string{encScheme, encAddress, encPort, encPool, encName}, "_"), nil
}

func decodeVolID(volID string) (*volInfo, error) {
	v := &volInfo{}
	volIDs := strings.SplitN(volID, "_", 5)

	if len(volIDs) != 5 {
		return nil, fmt.Errorf("Failed to decode information from %s: not enough fields", volID)
	}

	schemeByte, err := base64.RawStdEncoding.DecodeString(strings.ReplaceAll(volIDs[0], "-", "/"))
	if err != nil {
		return nil, fmt.Errorf("Failed to decode scheme information from %s: %v", volID, err)
	}
	v.scheme = string(schemeByte)

	addressByte, err := base64.RawStdEncoding.DecodeString(strings.ReplaceAll(volIDs[1], "-", "/"))
	if err != nil {
		return nil, fmt.Errorf("Failed to decode address information from %s: %v", volID, err)
	}
	v.address = string(addressByte)

	portByte, err := base64.RawStdEncoding.DecodeString(strings.ReplaceAll(volIDs[2], "-", "/"))
	if err != nil {
		return nil, fmt.Errorf("Failed to decode port information from %s: %v", volID, err)
	}
	v.port = string(portByte)

	poolByte, err := base64.RawStdEncoding.DecodeString(strings.ReplaceAll(volIDs[3], "-", "/"))
	if err != nil {
		return nil, fmt.Errorf("Failed to decode pool information from %s: %v", volID, err)
	}
	v.pool = string(poolByte)

	nameByte, err := base64.RawStdEncoding.DecodeString(strings.ReplaceAll(volIDs[4], "-", "/"))
	if err != nil {
		return nil, fmt.Errorf("Failed to decode name information from %s: %v", volID, err)
	}
	v.name = string(nameByte)

	return v, nil
}

type targetdClient struct {
	targetdURL string
}

// NewtargetdClient creates new iscsi provisioner
func NewtargetdClient(url string) *targetdClient {
	return &targetdClient{
		targetdURL: url,
	}
}

type export struct {
	InitiatorWwn string `json:"initiator_wwn"`
	Lun          int32  `json:"lun"`
	VolName      string `json:"vol_name"`
	VolSize      int    `json:"vol_size"`
	VolUUID      string `json:"vol_uuid"`
	Pool         string `json:"pool"`
}

type exportList []export

type volCreateArgs struct {
	Pool string `json:"pool"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type volDestroyArgs struct {
	Pool string `json:"pool"`
	Name string `json:"name"`
}

type exportCreateArgs struct {
	Pool         string `json:"pool"`
	Vol          string `json:"vol"`
	InitiatorWwn string `json:"initiator_wwn"`
	Lun          int32  `json:"lun"`
}

type exportDestroyArgs struct {
	Pool         string `json:"pool"`
	Vol          string `json:"vol"`
	InitiatorWwn string `json:"initiator_wwn"`
}

func genTargetdURL(v volInfo) string {
	return fmt.Sprintf("%s://%s:%s@%s:%s/targetrpc", v.scheme, v.username, v.password, v.address, v.port)
}

// volDestroy removes calls vol_destroy targetd API to remove volume.
func (t *targetdClient) volDestroy(vol string, pool string) error {
	client, err := t.getConnection()
	defer client.Close()
	if err != nil {
		glog.Warningf("Failed to destroy volume %s in pool %s: %v", vol, pool, err)
		return err
	}
	args := volDestroyArgs{
		Pool: pool,
		Name: vol,
	}
	err = client.Call("vol_destroy", args, nil)
	return err
}

// exportDestroy calls export_destroy targetd API to remove export of volume.
func (t *targetdClient) exportDestroy(vol string, pool string, initiator string) error {
	client, err := t.getConnection()
	defer client.Close()
	if err != nil {
		glog.Warningf("Failed to destroy export for volume %s in pool %s from initiator %s: %v", vol, pool, initiator, err)
		return err
	}
	args := exportDestroyArgs{
		Pool:         pool,
		Vol:          vol,
		InitiatorWwn: initiator,
	}
	err = client.Call("export_destroy", args, nil)
	return err
}

// volCreate calls vol_create targetd API to create a volume.
func (t *targetdClient) volCreate(name string, size int64, pool string) error {
	client, err := t.getConnection()
	defer client.Close()
	if err != nil {
		glog.Warningf("Failed to create volume %s in pool %s with size %d: %v", name, pool, size, err)
		return err
	}
	args := volCreateArgs{
		Pool: pool,
		Name: name,
		Size: size,
	}
	err = client.Call("vol_create", args, nil)
	return err
}

// exportCreate calls export_create targetd API to create an export of volume.
func (t *targetdClient) exportCreate(vol string, lun int32, pool string, initiator string) error {
	client, err := t.getConnection()
	defer client.Close()
	if err != nil {
		glog.Warningf("Failed to create export for volume %s in pool %s to lun %d in initiator %s: %v", vol, pool, lun, initiator, err)
		return err
	}
	args := exportCreateArgs{
		Pool:         pool,
		Vol:          vol,
		InitiatorWwn: initiator,
		Lun:          lun,
	}
	err = client.Call("export_create", args, nil)
	return err
}

// exportList lists calls export_list targetd API to get export objects.
func (t *targetdClient) exportList() (exportList, error) {
	client, err := t.getConnection()
	defer client.Close()
	if err != nil {
		glog.Warningf("Failed to list export")
		return nil, err
	}
	var result1 exportList
	err = client.Call("export_list", nil, &result1)
	return result1, err
}

func (t *targetdClient) getConnection() (*jsonrpc2.Client, error) {
	glog.Infof("opening connection to targetd: ", t.targetdURL)

	client := jsonrpc2.NewHTTPClient(t.targetdURL)
	if client == nil {
		glog.Warningf("error creating the connection to targetd", t.targetdURL)
		return nil, errors.New("error creating the connection to targetd")
	}
	glog.Infof("targetd client created")
	return client, nil
}
