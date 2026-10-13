package vergeos

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	vergeos "github.com/verge-io/govergeos"
	v1 "k8s.io/api/core/v1"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

const providerPrefix = "vergeos://"

// errVMNotFound is wrapped by lookups that match no (non-snapshot) VM.
var errVMNotFound = errors.New("VM not found")

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type instances struct {
	vms           VMClient
	machineStatus MachineStatusClient
	nics          VMNICClient
	// nodeNetworkID, when non-zero, limits NIC-lease InternalIPs to this vnet.
	nodeNetworkID int
}

func newInstances(vms VMClient, machineStatus MachineStatusClient, nics VMNICClient, nodeNetworkID int) *instances {
	return &instances{vms: vms, machineStatus: machineStatus, nics: nics, nodeNetworkID: nodeNetworkID}
}

// vmRef identifies a VM from a provider ID: either a VM UUID or a legacy integer $key.
type vmRef struct {
	uuid string // lower-case; set for vergeos://<uuid>
	key  int    // set for legacy vergeos://<int>
}

// parseProviderID parses "vergeos://<vm-uuid>" or the legacy "vergeos://<vm-key>".
func parseProviderID(providerID string) (vmRef, error) {
	if !strings.HasPrefix(providerID, providerPrefix) {
		return vmRef{}, fmt.Errorf("invalid provider ID %q: expected prefix %q", providerID, providerPrefix)
	}
	idStr := strings.TrimPrefix(providerID, providerPrefix)
	if idStr == "" {
		return vmRef{}, fmt.Errorf("invalid provider ID %q: missing VM ID", providerID)
	}
	if uuidRe.MatchString(idStr) {
		return vmRef{uuid: strings.ToLower(idStr)}, nil
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return vmRef{}, fmt.Errorf("invalid provider ID %q: expected a VM UUID or integer ID", providerID)
	}
	return vmRef{key: id}, nil
}

// formatProviderID creates the canonical provider ID from a VM UUID.
func formatProviderID(vmUUID string) string {
	return providerPrefix + vmUUID
}

// formatInstanceType creates an instance type string from CPU cores and RAM.
func formatInstanceType(cpuCores, ramMB int) string {
	return fmt.Sprintf("%dcpu-%dmb", cpuCores, ramMB)
}

// findVM lists non-snapshot VMs matching field eq value.
// Returns errVMNotFound for no match and an error for more than one.
func (i *instances) findVM(ctx context.Context, field, value string) (*vergeos.VM, error) {
	filter := fmt.Sprintf("%s eq '%s' and is_snapshot eq false", field, value)
	vms, err := i.vms.List(ctx, vergeos.WithFilter(filter))
	if err != nil {
		return nil, fmt.Errorf("listing VMs by %s %q: %w", field, value, err)
	}
	switch len(vms) {
	case 0:
		return nil, fmt.Errorf("%w: no VM with %s %q", errVMNotFound, field, value)
	case 1:
		return &vms[0], nil
	default:
		return nil, fmt.Errorf("ambiguous: %d VMs match %s %q", len(vms), field, value)
	}
}

// getVM resolves a Kubernetes node to a VergeOS VM.
// With a vergeos:// providerID it looks up by UUID (or legacy integer key).
// Otherwise (no providerID, or one set by another system such as rke2://) it
// tries node.status.nodeInfo.systemUUID against the VM UUID, then the node name.
// Snapshots are never matched and ambiguous matches are errors.
func (i *instances) getVM(ctx context.Context, node *v1.Node) (*vergeos.VM, error) {
	if strings.HasPrefix(node.Spec.ProviderID, providerPrefix) {
		ref, err := parseProviderID(node.Spec.ProviderID)
		if err != nil {
			return nil, err
		}
		if ref.uuid != "" {
			return i.findVM(ctx, "uuid", ref.uuid)
		}
		vm, err := i.vms.Get(ctx, ref.key)
		if err != nil {
			return nil, err
		}
		if vm.IsSnapshot {
			return nil, fmt.Errorf("%w: VM %d is a snapshot", errVMNotFound, ref.key)
		}
		return vm, nil
	}

	if node.Spec.ProviderID != "" {
		klog.V(2).Infof("Node %q has non-vergeos provider ID %q, falling back to systemUUID/name lookup", node.Name, node.Spec.ProviderID)
	}

	if sysUUID := node.Status.NodeInfo.SystemUUID; uuidRe.MatchString(sysUUID) {
		vm, err := i.findVM(ctx, "uuid", strings.ToLower(sysUUID))
		if err == nil {
			return vm, nil
		}
		if !errors.Is(err, errVMNotFound) {
			return nil, err
		}
		klog.V(2).Infof("Node %q: no VM with UUID %s, trying name", node.Name, sysUUID)
	}

	vm, err := i.findVM(ctx, "name", node.Name)
	if err != nil {
		return nil, fmt.Errorf("resolving node %q: %w", node.Name, err)
	}
	return vm, nil
}

func (i *instances) InstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	_, err := i.getVM(ctx, node)
	if err != nil {
		if isVMNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (i *instances) InstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	vm, err := i.getVM(ctx, node)
	if err != nil {
		if isVMNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return !vm.PowerState, nil
}

func isVMNotFound(err error) bool {
	return vergeos.IsNotFoundError(err) || errors.Is(err, errVMNotFound)
}

func usableIP(s string) (net.IP, bool) {
	ip := net.ParseIP(s)
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return nil, false
	}
	return ip, true
}

// guestAgentIPs returns IPv4 addresses reported by qemu-guest-agent, if any.
func (i *instances) guestAgentIPs(ctx context.Context, vm *vergeos.VM) []string {
	if i.machineStatus == nil {
		return nil
	}
	status, err := i.machineStatus.Get(ctx, vm.Machine)
	if err != nil {
		klog.Warningf("Failed to get machine status for VM %d (machine %d): %v", vm.ID, vm.Machine, err)
		return nil
	}
	if status.AgentGuestInfo == nil {
		return nil
	}
	var ips []string
	for _, iface := range status.AgentGuestInfo.Network {
		for _, addr := range iface.IPAddresses {
			if addr.Type != "ipv4" || addr.Address == "" {
				continue
			}
			if _, ok := usableIP(addr.Address); !ok {
				continue
			}
			ips = append(ips, addr.Address)
		}
	}
	return ips
}

// nicLeaseIPs returns machine_nics.ipaddress values (DHCP leases) ordered by NIC key,
// limited to nodeNetworkID when configured.
func (i *instances) nicLeaseIPs(ctx context.Context, vm *vergeos.VM) ([]string, error) {
	if i.nics == nil {
		return nil, nil
	}
	nics, err := i.nics.List(ctx, vm.Machine)
	if err != nil {
		return nil, fmt.Errorf("listing NICs for VM %d (machine %d): %w", vm.ID, vm.Machine, err)
	}
	sort.SliceStable(nics, func(a, b int) bool { return nics[a].ID < nics[b].ID })
	var ips []string
	for _, nic := range nics {
		if i.nodeNetworkID != 0 && nic.VNET.Int() != i.nodeNetworkID {
			continue
		}
		if _, ok := usableIP(nic.IPAddress); !ok {
			continue
		}
		ips = append(ips, nic.IPAddress)
	}
	return ips, nil
}

func (i *instances) InstanceMetadata(ctx context.Context, node *v1.Node) (*cloudprovider.InstanceMetadata, error) {
	vm, err := i.getVM(ctx, node)
	if err != nil {
		return nil, err
	}

	// Guest agent data first; the stock Ubuntu image has no agent, so fall back
	// to the NIC DHCP leases VergeOS already tracks.
	ips := i.guestAgentIPs(ctx, vm)
	if len(ips) == 0 {
		ips, err = i.nicLeaseIPs(ctx, vm)
		if err != nil {
			return nil, err
		}
	}

	addresses := make([]v1.NodeAddress, 0, len(ips)+1)
	seen := map[string]bool{}
	for _, ip := range ips {
		if seen[ip] {
			continue
		}
		seen[ip] = true
		addresses = append(addresses, v1.NodeAddress{Type: v1.NodeInternalIP, Address: ip})
	}

	// Hostname is the node's own name: VergeOS VM name == node name for CAPI nodes,
	// and echoing it back avoids replacing the kubelet's value with a different one.
	addresses = append(addresses, v1.NodeAddress{Type: v1.NodeHostName, Address: node.Name})

	providerID := formatProviderID(vm.UUID)
	if vm.UUID == "" {
		providerID = providerPrefix + strconv.Itoa(int(vm.ID))
	}

	return &cloudprovider.InstanceMetadata{
		ProviderID:    providerID,
		InstanceType:  formatInstanceType(vm.CPUCores, vm.RAM),
		NodeAddresses: addresses,
		Zone:          "",
		Region:        "",
	}, nil
}
