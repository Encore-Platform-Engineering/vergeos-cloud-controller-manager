package vergeos

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	vergeos "github.com/verge-io/govergeos"
	v1 "k8s.io/api/core/v1"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

const providerPrefix = "vergeos://"

type instances struct {
	vms           VMClient
	machineStatus MachineStatusClient
}

func newInstances(vms VMClient, machineStatus MachineStatusClient) *instances {
	return &instances{vms: vms, machineStatus: machineStatus}
}

// parseProviderID extracts the VM ID from a provider ID string.
// Format: "vergeos://<vm-id>"
func parseProviderID(providerID string) (int, error) {
	if !strings.HasPrefix(providerID, providerPrefix) {
		return 0, fmt.Errorf("invalid provider ID %q: expected prefix %q", providerID, providerPrefix)
	}
	idStr := strings.TrimPrefix(providerID, providerPrefix)
	if idStr == "" {
		return 0, fmt.Errorf("invalid provider ID %q: missing VM ID", providerID)
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return 0, fmt.Errorf("invalid provider ID %q: %w", providerID, err)
	}
	return id, nil
}

// formatProviderID creates a provider ID string from a VM ID.
func formatProviderID(vmID int) string {
	return fmt.Sprintf("%s%d", providerPrefix, vmID)
}

// formatInstanceType creates an instance type string from CPU cores and RAM.
func formatInstanceType(cpuCores, ramMB int) string {
	return fmt.Sprintf("%dcpu-%dmb", cpuCores, ramMB)
}

// getVM resolves a Kubernetes node to a VergeOS VM.
// Uses providerID if it has the vergeos:// prefix, otherwise falls back to name lookup.
// This handles nodes provisioned by other systems (e.g., Rancher RKE2 with rke2:// prefix).
func (i *instances) getVM(ctx context.Context, node *v1.Node) (*vergeos.VM, error) {
	if strings.HasPrefix(node.Spec.ProviderID, providerPrefix) {
		vmID, err := parseProviderID(node.Spec.ProviderID)
		if err != nil {
			return nil, err
		}
		return i.vms.Get(ctx, vmID)
	}

	if node.Spec.ProviderID != "" {
		klog.V(2).Infof("Node %q has non-vergeos provider ID %q, falling back to name lookup", node.Name, node.Spec.ProviderID)
	}

	// Fall back to name lookup.
	vms, err := i.vms.List(ctx, vergeos.WithFilter(fmt.Sprintf("name eq '%s'", node.Name)))
	if err != nil {
		return nil, fmt.Errorf("listing VMs by name %q: %w", node.Name, err)
	}
	if len(vms) == 0 {
		return nil, fmt.Errorf("VM not found for node %q", node.Name)
	}
	if len(vms) > 1 {
		klog.Warningf("Multiple VMs found for node %q, using first match (ID=%d)", node.Name, vms[0].ID)
	}
	return &vms[0], nil
}

func (i *instances) InstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	_, err := i.getVM(ctx, node)
	if err != nil {
		if vergeos.IsNotFoundError(err) {
			return false, nil
		}
		// Name lookup returns a plain error when no VMs match.
		if !strings.HasPrefix(node.Spec.ProviderID, providerPrefix) && strings.Contains(err.Error(), "VM not found") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (i *instances) InstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	vm, err := i.getVM(ctx, node)
	if err != nil {
		if vergeos.IsNotFoundError(err) || strings.Contains(err.Error(), "VM not found") {
			return false, nil
		}
		return false, err
	}
	return !vm.PowerState, nil
}

func (i *instances) InstanceMetadata(ctx context.Context, node *v1.Node) (*cloudprovider.InstanceMetadata, error) {
	vm, err := i.getVM(ctx, node)
	if err != nil {
		return nil, err
	}

	addresses := []v1.NodeAddress{}

	// Get IPs from guest agent via MachineStatusService (most reliable for DHCP environments).
	if i.machineStatus != nil {
		status, err := i.machineStatus.Get(ctx, vm.Machine)
		if err != nil {
			klog.Warningf("Failed to get machine status for VM %d (machine %d): %v", vm.ID, vm.Machine, err)
		} else if status.AgentGuestInfo != nil {
			for _, iface := range status.AgentGuestInfo.Network {
				for _, addr := range iface.IPAddresses {
					if addr.Type != "ipv4" || addr.Address == "" {
						continue
					}
					ip := net.ParseIP(addr.Address)
					if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
						continue
					}
					addresses = append(addresses, v1.NodeAddress{
						Type:    v1.NodeInternalIP,
						Address: addr.Address,
					})
				}
			}
		}
	}

	// Add hostname.
	addresses = append(addresses, v1.NodeAddress{
		Type:    v1.NodeHostName,
		Address: vm.Name,
	})

	return &cloudprovider.InstanceMetadata{
		ProviderID:    formatProviderID(int(vm.ID)),
		InstanceType:  formatInstanceType(vm.CPUCores, vm.RAM),
		NodeAddresses: addresses,
		Zone:          "",
		Region:        "",
	}, nil
}
