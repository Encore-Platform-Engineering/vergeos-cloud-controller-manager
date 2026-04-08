package vergeos

import (
	"context"
	"fmt"
	"testing"

	vergeos "github.com/verge-io/govergeos"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Mock VMClient for testing.
type mockVMClient struct {
	getFunc  func(ctx context.Context, id int) (*vergeos.VM, error)
	listFunc func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error)
}

func (m *mockVMClient) Get(ctx context.Context, id int) (*vergeos.VM, error) {
	return m.getFunc(ctx, id)
}

func (m *mockVMClient) List(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
	return m.listFunc(ctx, opts...)
}

// Mock MachineStatusClient for testing.
type mockMachineStatusClient struct {
	getFunc func(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error)
}

func (m *mockMachineStatusClient) Get(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error) {
	return m.getFunc(ctx, machineKey)
}

// testNode creates a v1.Node for testing.
func testNode(name, providerID string) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1.NodeSpec{ProviderID: providerID},
	}
}

func TestParseProviderID(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		wantID     int
		wantErr    bool
	}{
		{"valid", "vergeos://42", 42, false},
		{"valid large", "vergeos://12345", 12345, false},
		{"wrong scheme", "aws://42", 0, true},
		{"missing id", "vergeos://", 0, true},
		{"not a number", "vergeos://abc", 0, true},
		{"empty", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := parseProviderID(tt.providerID)
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("expected id=%d, got %d", tt.wantID, id)
			}
		})
	}
}

func TestFormatProviderID(t *testing.T) {
	got := formatProviderID(42)
	want := "vergeos://42"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestFormatInstanceType(t *testing.T) {
	tests := []struct {
		cpu  int
		ram  int
		want string
	}{
		{4, 8192, "4cpu-8192mb"},
		{1, 512, "1cpu-512mb"},
		{16, 65536, "16cpu-65536mb"},
	}
	for _, tt := range tests {
		got := formatInstanceType(tt.cpu, tt.ram)
		if got != tt.want {
			t.Errorf("formatInstanceType(%d, %d) = %q, want %q", tt.cpu, tt.ram, got, tt.want)
		}
	}
}

func TestGetVM_ByProviderID(t *testing.T) {
	vm := &vergeos.VM{ID: 42, Name: "node-1", CPUCores: 4, RAM: 8192}

	i := &instances{
		vms: &mockVMClient{
			getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
				if id == 42 {
					return vm, nil
				}
				return nil, fmt.Errorf("not found")
			},
		},
	}

	got, err := i.getVM(context.Background(), testNode("node-1", "vergeos://42"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "node-1" {
		t.Errorf("expected name=node-1, got %s", got.Name)
	}
}

func TestGetVM_ByName(t *testing.T) {
	vm := vergeos.VM{ID: 42, Name: "node-1", CPUCores: 4, RAM: 8192}

	i := &instances{
		vms: &mockVMClient{
			listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return []vergeos.VM{vm}, nil
			},
		},
	}

	got, err := i.getVM(context.Background(), testNode("node-1", ""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if int(got.ID) != 42 {
		t.Errorf("expected ID=42, got %d", got.ID)
	}
}

func TestGetVM_ByNameNotFound(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return []vergeos.VM{}, nil
			},
		},
	}

	_, err := i.getVM(context.Background(), testNode("nonexistent", ""))
	if err == nil {
		t.Error("expected error for VM not found by name")
	}
}

func TestGetVM_NonVergeOSProviderID_FallsBackToName(t *testing.T) {
	vm := vergeos.VM{ID: 42, Name: "node-1", CPUCores: 4, RAM: 8192}

	i := &instances{
		vms: &mockVMClient{
			listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return []vergeos.VM{vm}, nil
			},
		},
	}

	// RKE2-style provider ID should fall back to name lookup.
	got, err := i.getVM(context.Background(), testNode("node-1", "rke2://node-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if int(got.ID) != 42 {
		t.Errorf("expected ID=42, got %d", got.ID)
	}
}

func TestInstanceExists_ByProviderID(t *testing.T) {
	vm := &vergeos.VM{ID: 42, Name: "node-1"}

	i := &instances{
		vms: &mockVMClient{
			getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
				if id == 42 {
					return vm, nil
				}
				return nil, &vergeos.NotFoundError{Resource: "vm", ID: id}
			},
		},
	}

	exists, err := i.InstanceExists(context.Background(), testNode("node-1", "vergeos://42"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected instance to exist")
	}
}

func TestInstanceExists_NotFound(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
				return nil, &vergeos.NotFoundError{Resource: "vm", ID: id}
			},
		},
	}

	exists, err := i.InstanceExists(context.Background(), testNode("node-1", "vergeos://999"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected instance to not exist")
	}
}

func TestInstanceExists_ByName(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return []vergeos.VM{{ID: 42, Name: "node-1"}}, nil
			},
		},
	}

	exists, err := i.InstanceExists(context.Background(), testNode("node-1", ""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected instance to exist")
	}
}

func TestInstanceExists_ByNameNotFound(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return []vergeos.VM{}, nil
			},
		},
	}

	exists, err := i.InstanceExists(context.Background(), testNode("gone", ""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected instance to not exist")
	}
}

func TestInstanceExists_NonVergeOSProviderID(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return []vergeos.VM{{ID: 42, Name: "node-1"}}, nil
			},
		},
	}

	exists, err := i.InstanceExists(context.Background(), testNode("node-1", "rke2://node-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected instance to exist via name fallback")
	}
}

func TestInstanceExists_NonVergeOSProviderID_NotFound(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return []vergeos.VM{}, nil
			},
		},
	}

	exists, err := i.InstanceExists(context.Background(), testNode("gone", "rke2://gone"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected instance to not exist")
	}
}

func TestInstanceShutdown_Running(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
				return &vergeos.VM{ID: 42, PowerState: true}, nil
			},
		},
	}

	shutdown, err := i.InstanceShutdown(context.Background(), testNode("node-1", "vergeos://42"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shutdown {
		t.Error("expected instance to not be shutdown (PowerState=true means running)")
	}
}

func TestInstanceShutdown_Stopped(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
				return &vergeos.VM{ID: 42, PowerState: false}, nil
			},
		},
	}

	shutdown, err := i.InstanceShutdown(context.Background(), testNode("node-1", "vergeos://42"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !shutdown {
		t.Error("expected instance to be shutdown (PowerState=false means stopped)")
	}
}

func TestInstanceMetadata(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
				return &vergeos.VM{ID: 42, Name: "node-1", CPUCores: 4, RAM: 8192, Machine: 71}, nil
			},
		},
		machineStatus: &mockMachineStatusClient{
			getFunc: func(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error) {
				if machineKey != 71 {
					t.Errorf("expected Get for machine 71, got %d", machineKey)
				}
				return &vergeos.MachineStatus{
					AgentGuestInfo: &vergeos.GuestInfo{
						Network: []vergeos.GuestNetworkInterface{
							{
								Name: "ens1",
								IPAddresses: []vergeos.GuestIPAddress{
									{Type: "ipv4", Address: "10.0.0.5"},
								},
							},
							{
								Name: "ens2",
								IPAddresses: []vergeos.GuestIPAddress{
									{Type: "ipv4", Address: "10.0.1.10"},
								},
							},
						},
					},
				}, nil
			},
		},
	}

	meta, err := i.InstanceMetadata(context.Background(), testNode("node-1", "vergeos://42"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.ProviderID != "vergeos://42" {
		t.Errorf("expected providerID=vergeos://42, got %s", meta.ProviderID)
	}
	if meta.InstanceType != "4cpu-8192mb" {
		t.Errorf("expected instanceType=4cpu-8192mb, got %s", meta.InstanceType)
	}
	// Should have 2 IPs + 1 hostname = 3 addresses
	if len(meta.NodeAddresses) != 3 {
		t.Fatalf("expected 3 addresses, got %d", len(meta.NodeAddresses))
	}
	// First two are InternalIP
	if meta.NodeAddresses[0].Type != v1.NodeInternalIP || meta.NodeAddresses[0].Address != "10.0.0.5" {
		t.Errorf("expected first address=10.0.0.5 InternalIP, got %v", meta.NodeAddresses[0])
	}
	if meta.NodeAddresses[1].Type != v1.NodeInternalIP || meta.NodeAddresses[1].Address != "10.0.1.10" {
		t.Errorf("expected second address=10.0.1.10 InternalIP, got %v", meta.NodeAddresses[1])
	}
	// Last is hostname
	if meta.NodeAddresses[2].Type != v1.NodeHostName || meta.NodeAddresses[2].Address != "node-1" {
		t.Errorf("expected third address=node-1 Hostname, got %v", meta.NodeAddresses[2])
	}
}

func TestInstanceMetadata_NoNICs(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
				return &vergeos.VM{ID: 42, Name: "node-1", CPUCores: 2, RAM: 4096, Machine: 71}, nil
			},
		},
		machineStatus: &mockMachineStatusClient{
			getFunc: func(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error) {
				return &vergeos.MachineStatus{}, nil // No guest agent info
			},
		},
	}

	meta, err := i.InstanceMetadata(context.Background(), testNode("node-1", "vergeos://42"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have just the hostname
	if len(meta.NodeAddresses) != 1 {
		t.Fatalf("expected 1 address (hostname only), got %d", len(meta.NodeAddresses))
	}
	if meta.NodeAddresses[0].Type != v1.NodeHostName {
		t.Errorf("expected hostname address, got %v", meta.NodeAddresses[0].Type)
	}
}

func TestInstanceMetadata_NonVergeOSProviderID(t *testing.T) {
	i := &instances{
		vms: &mockVMClient{
			listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return []vergeos.VM{{ID: 42, Name: "node-1", CPUCores: 4, RAM: 8192, Machine: 71}}, nil
			},
		},
		machineStatus: &mockMachineStatusClient{
			getFunc: func(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error) {
				return &vergeos.MachineStatus{
					AgentGuestInfo: &vergeos.GuestInfo{
						Network: []vergeos.GuestNetworkInterface{
							{
								Name:        "ens1",
								IPAddresses: []vergeos.GuestIPAddress{{Type: "ipv4", Address: "10.8.8.50"}},
							},
						},
					},
				}, nil
			},
		},
	}

	meta, err := i.InstanceMetadata(context.Background(), testNode("node-1", "rke2://node-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return vergeos provider ID (overwrites rke2://)
	if meta.ProviderID != "vergeos://42" {
		t.Errorf("expected providerID=vergeos://42, got %s", meta.ProviderID)
	}
}
