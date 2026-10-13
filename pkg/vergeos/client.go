package vergeos

import (
	"context"

	vergeos "github.com/verge-io/govergeos"
)

// VMClient abstracts the govergeos VMs API for testing.
type VMClient interface {
	Get(ctx context.Context, id int) (*vergeos.VM, error)
	List(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error)
}

// MachineStatusClient abstracts the govergeos MachineStatus API for testing.
type MachineStatusClient interface {
	Get(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error)
}

// VMNICClient abstracts the govergeos VMNICs API for testing.
// List takes the VM's Machine ID (not its $key).
type VMNICClient interface {
	List(ctx context.Context, machineID int) ([]vergeos.VMNIC, error)
}

// VNetAddressClient abstracts the govergeos VNetAddresses API for testing.
type VNetAddressClient interface {
	GetByIP(ctx context.Context, vnetID int, ip string) (*vergeos.VNetAddress, error)
	ListByNetwork(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetAddress, error)
	Create(ctx context.Context, req *vergeos.VNetAddressCreateRequest) (*vergeos.VNetAddress, error)
	Update(ctx context.Context, id int, req *vergeos.VNetAddressUpdateRequest) (*vergeos.VNetAddress, error)
	Delete(ctx context.Context, id int) error
}

// VNetRuleClient abstracts the govergeos VNetRules API for testing.
type VNetRuleClient interface {
	ListByNetwork(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error)
	GetByName(ctx context.Context, vnetID int, name string) (*vergeos.VNetRule, error)
	Create(ctx context.Context, req *vergeos.VNetRuleCreateRequest) (*vergeos.VNetRule, error)
	Update(ctx context.Context, id int, req *vergeos.VNetRuleUpdateRequest) (*vergeos.VNetRule, error)
	Delete(ctx context.Context, id int) error
}

// NetworkClient abstracts the govergeos Networks API for testing.
type NetworkClient interface {
	Get(ctx context.Context, id int) (*vergeos.Network, error)
	ApplyRules(ctx context.Context, id int) error
}
