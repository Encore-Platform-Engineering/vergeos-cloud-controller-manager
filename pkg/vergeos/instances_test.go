package vergeos

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

const testUUID = "3f2b8c1e-5a47-4d9e-9b1a-0c6d7e8f9a10"

func TestParseProviderID(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		want       vmRef
		wantErr    bool
	}{
		{"uuid", "vergeos://" + testUUID, vmRef{uuid: testUUID}, false},
		{"uuid upper-case is normalised", "vergeos://" + strings.ToUpper(testUUID), vmRef{uuid: testUUID}, false},
		{"legacy int", "vergeos://42", vmRef{key: 42}, false},
		{"legacy large int", "vergeos://12345", vmRef{key: 12345}, false},
		{"wrong scheme", "aws://42", vmRef{}, true},
		{"missing id", "vergeos://", vmRef{}, true},
		{"not a number or uuid", "vergeos://abc", vmRef{}, true},
		{"truncated uuid", "vergeos://" + testUUID[:30], vmRef{}, true},
		{"uuid with filter injection", "vergeos://" + testUUID + "' or 1 eq 1", vmRef{}, true},
		{"empty", "", vmRef{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProviderID(tt.providerID)
			if tt.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFormatProviderID(t *testing.T) {
	got := formatProviderID(testUUID)
	want := "vergeos://" + testUUID
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

// leaseNICs returns one NIC with a DHCP lease, for tests about everything but addresses.
func leaseNICs() *mockNICClient {
	return &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
		return []vergeos.VMNIC{{ID: 1, VNET: 10, MAC: "52:54:00:00:00:01", IPAddress: "10.1.101.50"}}, nil
	}}
}

// Guest-agent IPs are used for NICs without a lease, matched by MAC.
func TestInstanceMetadata(t *testing.T) {
	i := &instances{
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			return []vergeos.VMNIC{
				{ID: 8, VNET: 10, MAC: "52:54:00:00:00:02"},
				{ID: 7, VNET: 10, MAC: "52:54:00:00:00:01"},
			}, nil
		}},
		vms: &mockVMClient{
			getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
				return &vergeos.VM{ID: 42, UUID: testUUID, Name: "node-1", CPUCores: 4, RAM: 8192, Machine: 71}, nil
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
								Name:            "ens1",
								HardwareAddress: "52:54:00:00:00:01",
								IPAddresses: []vergeos.GuestIPAddress{
									{Type: "ipv4", Address: "10.0.0.5"},
								},
							},
							{
								Name:            "ens2",
								HardwareAddress: "52:54:00:00:00:02",
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
	if meta.ProviderID != "vergeos://"+testUUID {
		t.Errorf("expected providerID=vergeos://%s, got %s", testUUID, meta.ProviderID)
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

// No lease and no agent data (DHCP not populated yet at boot): must error so the
// node stays uninitialized and is retried, not succeed with only a Hostname.
func TestInstanceMetadata_NoIPsIsError(t *testing.T) {
	tests := []struct {
		name string
		nics []vergeos.VMNIC
	}{
		{"no NICs", nil},
		{"NIC without lease", []vergeos.VMNIC{{ID: 1, VNET: 10, MAC: "52:54:00:00:00:01"}}},
		{"only link-local lease", []vergeos.VMNIC{{ID: 1, VNET: 10, IPAddress: "169.254.3.4"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := &instances{
				vms:           metaVM(),
				machineStatus: noAgent(),
				nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
					return tt.nics, nil
				}},
			}
			meta, err := i.InstanceMetadata(context.Background(), testNode("node-1", "vergeos://42"))
			if err == nil {
				t.Fatalf("expected error, got addresses %v", meta.NodeAddresses)
			}
			if !strings.Contains(err.Error(), "no InternalIP") {
				t.Errorf("unexpected error text: %v", err)
			}
		})
	}
}

func TestInstanceMetadata_NonVergeOSProviderID(t *testing.T) {
	i := &instances{
		nics: leaseNICs(),
		vms: &mockVMClient{
			listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return []vergeos.VM{{ID: 42, UUID: testUUID, Name: "node-1", CPUCores: 4, RAM: 8192, Machine: 71}}, nil
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
	if meta.ProviderID != "vergeos://"+testUUID {
		t.Errorf("expected providerID=vergeos://%s, got %s", testUUID, meta.ProviderID)
	}
}

// appliedFilter returns the filter expression a ListOption set carries.
func appliedFilter(opts []vergeos.ListOption) string {
	var o vergeos.ListOptions
	for _, f := range opts {
		f(&o)
	}
	return o.Filter
}

// fakeVMs is a VMClient whose List answers by exact filter string.
type fakeVMs struct {
	byFilter map[string][]vergeos.VM
	byKey    map[int]*vergeos.VM
	filters  []string
}

func (f *fakeVMs) Get(ctx context.Context, id int) (*vergeos.VM, error) {
	if vm, ok := f.byKey[id]; ok {
		return vm, nil
	}
	return nil, &vergeos.NotFoundError{Resource: "vm", ID: id}
}

func (f *fakeVMs) List(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
	flt := appliedFilter(opts)
	f.filters = append(f.filters, flt)
	return f.byFilter[flt], nil
}

func uuidFilter(u string) string {
	return fmt.Sprintf("uuid eq '%s' and is_snapshot eq false", u)
}

func nameFilter(n string) string {
	return fmt.Sprintf("name eq '%s' and is_snapshot eq false", n)
}

func TestGetVM_ByUUIDProviderID(t *testing.T) {
	f := &fakeVMs{byFilter: map[string][]vergeos.VM{
		uuidFilter(testUUID): {{ID: 7, UUID: testUUID, Name: "ns-m1"}},
	}}
	i := &instances{vms: f}

	got, err := i.getVM(context.Background(), testNode("whatever", "vergeos://"+strings.ToUpper(testUUID)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != 7 {
		t.Errorf("expected VM 7, got %d", got.ID)
	}
}

func TestGetVM_UUIDProviderID_NotFoundAndAmbiguous(t *testing.T) {
	f := &fakeVMs{byFilter: map[string][]vergeos.VM{
		uuidFilter(testUUID): {{ID: 1}, {ID: 2}},
	}}
	i := &instances{vms: f}
	node := testNode("n", "vergeos://"+testUUID)

	if _, err := i.getVM(context.Background(), node); err == nil || errors.Is(err, errVMNotFound) {
		t.Fatalf("expected non-notfound ambiguity error, got %v", err)
	}
	if exists, err := i.InstanceExists(context.Background(), node); err == nil || exists {
		t.Errorf("ambiguous match must surface as error, got exists=%v err=%v", exists, err)
	}

	missing := testNode("n", "vergeos://11111111-2222-3333-4444-555555555555")
	exists, err := i.InstanceExists(context.Background(), missing)
	if err != nil || exists {
		t.Errorf("unknown UUID: expected exists=false err=nil, got %v %v", exists, err)
	}
	down, err := i.InstanceShutdown(context.Background(), missing)
	if err != nil || down {
		t.Errorf("unknown UUID shutdown: expected false,nil got %v %v", down, err)
	}
}

func TestGetVM_LegacyIntProviderID_RejectsSnapshot(t *testing.T) {
	i := &instances{vms: &fakeVMs{byKey: map[int]*vergeos.VM{
		42: {ID: 42, Name: "n"},
		43: {ID: 43, Name: "n", IsSnapshot: true},
	}}}

	got, err := i.getVM(context.Background(), testNode("n", "vergeos://42"))
	if err != nil || got.ID != 42 {
		t.Fatalf("legacy key should resolve, got %v %v", got, err)
	}
	exists, err := i.InstanceExists(context.Background(), testNode("n", "vergeos://43"))
	if err != nil || exists {
		t.Errorf("snapshot VM must not exist, got %v %v", exists, err)
	}
}

func TestGetVM_NoProviderID_SystemUUIDThenName(t *testing.T) {
	f := &fakeVMs{byFilter: map[string][]vergeos.VM{
		uuidFilter(testUUID): {{ID: 7, UUID: testUUID, Name: "different-from-node"}},
		nameFilter("node-1"): {{ID: 9, Name: "node-1"}},
	}}
	i := &instances{vms: f}

	// systemUUID (SMBIOS, typically upper-case) wins over name.
	node := testNode("node-1", "")
	node.Status.NodeInfo.SystemUUID = strings.ToUpper(testUUID)
	got, err := i.getVM(context.Background(), node)
	if err != nil || got.ID != 7 {
		t.Fatalf("expected VM 7 via systemUUID, got %v %v", got, err)
	}
	if len(f.filters) != 1 {
		t.Errorf("name lookup must not run when systemUUID matches, filters: %v", f.filters)
	}

	// systemUUID with no VM falls through to name.
	node.Status.NodeInfo.SystemUUID = "11111111-2222-3333-4444-555555555555"
	got, err = i.getVM(context.Background(), node)
	if err != nil || got.ID != 9 {
		t.Fatalf("expected VM 9 via name, got %v %v", got, err)
	}

	// Garbage systemUUID is not sent to VergeOS.
	f.filters = nil
	node.Status.NodeInfo.SystemUUID = "not-a-uuid' or 1 eq 1"
	if _, err = i.getVM(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	if len(f.filters) != 1 || f.filters[0] != nameFilter("node-1") {
		t.Errorf("expected only the name lookup, got %v", f.filters)
	}
}

func TestGetVM_AmbiguousName(t *testing.T) {
	f := &fakeVMs{byFilter: map[string][]vergeos.VM{
		nameFilter("node-1"): {{ID: 1, Name: "node-1"}, {ID: 2, Name: "node-1"}},
	}}
	i := &instances{vms: f}

	_, err := i.getVM(context.Background(), testNode("node-1", ""))
	if err == nil {
		t.Fatal("expected error for ambiguous name")
	}
	if errors.Is(err, errVMNotFound) {
		t.Errorf("ambiguity must not look like not-found: %v", err)
	}
	if exists, err := i.InstanceExists(context.Background(), testNode("node-1", "")); err == nil || exists {
		t.Errorf("InstanceExists must error on ambiguity, got %v %v", exists, err)
	}
}

func TestGetVM_AmbiguousSystemUUID_DoesNotFallBackToName(t *testing.T) {
	f := &fakeVMs{byFilter: map[string][]vergeos.VM{
		uuidFilter(testUUID): {{ID: 1}, {ID: 2}},
		nameFilter("node-1"): {{ID: 9}},
	}}
	node := testNode("node-1", "")
	node.Status.NodeInfo.SystemUUID = testUUID
	if _, err := (&instances{vms: f}).getVM(context.Background(), node); err == nil {
		t.Fatal("expected error for ambiguous systemUUID")
	}
}

func TestGetVM_LookupsExcludeSnapshots(t *testing.T) {
	f := &fakeVMs{}
	i := &instances{vms: f}
	node := testNode("node-1", "")
	node.Status.NodeInfo.SystemUUID = testUUID
	_, _ = i.getVM(context.Background(), node)
	_, _ = i.getVM(context.Background(), testNode("n", "vergeos://"+testUUID))

	if len(f.filters) != 3 {
		t.Fatalf("expected 3 list calls, got %v", f.filters)
	}
	for _, flt := range f.filters {
		if !strings.Contains(flt, "is_snapshot eq false") {
			t.Errorf("filter %q does not exclude snapshots", flt)
		}
	}
}

type mockNICClient struct {
	list func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error)
}

func (m *mockNICClient) List(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
	return m.list(ctx, machineID)
}

func noAgent() *mockMachineStatusClient {
	return &mockMachineStatusClient{getFunc: func(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error) {
		return &vergeos.MachineStatus{}, nil
	}}
}

func metaVM() *mockVMClient {
	return &mockVMClient{getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
		return &vergeos.VM{ID: 42, UUID: testUUID, Name: "ns-machine", CPUCores: 2, RAM: 4096, Machine: 71}, nil
	}}
}

func TestInstanceMetadata_NICLeaseFallback(t *testing.T) {
	i := &instances{
		vms:           metaVM(),
		machineStatus: noAgent(),
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			if machineID != 71 {
				t.Errorf("expected machine 71, got %d", machineID)
			}
			// Deliberately unsorted; lowest $key must come first.
			return []vergeos.VMNIC{
				{ID: 9, VNET: 20, IPAddress: "10.9.0.5"},
				{ID: 3, VNET: 10, IPAddress: "10.1.101.50"},
				{ID: 5, VNET: 10, IPAddress: ""},
				{ID: 6, VNET: 10, IPAddress: "169.254.1.1"},
				{ID: 7, VNET: 10, IPAddress: "10.1.101.50"},
			}, nil
		}},
	}

	meta, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	want := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.1.101.50"},
		{Type: v1.NodeInternalIP, Address: "10.9.0.5"},
		{Type: v1.NodeHostName, Address: "ns-machine"},
	}
	if !reflect.DeepEqual(meta.NodeAddresses, want) {
		t.Errorf("addresses = %v, want %v", meta.NodeAddresses, want)
	}
	if meta.ProviderID != "vergeos://"+testUUID {
		t.Errorf("providerID = %s", meta.ProviderID)
	}
}

func TestInstanceMetadata_NICLeaseFallback_NoAgentClientAtAll(t *testing.T) {
	i := &instances{
		vms: metaVM(),
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			return []vergeos.VMNIC{{ID: 1, IPAddress: "10.1.101.50"}}, nil
		}},
	}
	meta, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.NodeAddresses[0] != (v1.NodeAddress{Type: v1.NodeInternalIP, Address: "10.1.101.50"}) {
		t.Errorf("got %v", meta.NodeAddresses)
	}
}

func TestInstanceMetadata_NICLeaseFallback_VNetFilter(t *testing.T) {
	i := &instances{
		vms:           metaVM(),
		machineStatus: noAgent(),
		nodeNetworkID: 10,
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			return []vergeos.VMNIC{
				{ID: 1, VNET: 20, IPAddress: "10.9.0.5"},
				{ID: 2, VNET: 10, IPAddress: "10.1.101.50"},
			}, nil
		}},
	}
	meta, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.NodeAddresses) != 2 || meta.NodeAddresses[0].Address != "10.1.101.50" {
		t.Errorf("expected only vnet 10 lease + hostname, got %v", meta.NodeAddresses)
	}
}

func TestInstanceMetadata_LeasesWinOverAgent(t *testing.T) {
	i := &instances{
		vms: metaVM(),
		machineStatus: &mockMachineStatusClient{getFunc: func(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error) {
			t.Error("guest agent must not be consulted when NIC leases give an IP")
			return nil, nil
		}},
		nics: leaseNICs(),
	}
	meta, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.NodeAddresses) != 2 || meta.NodeAddresses[0].Address != "10.1.101.50" {
		t.Errorf("got %v", meta.NodeAddresses)
	}
}

func agentWith(ifaces ...vergeos.GuestNetworkInterface) *mockMachineStatusClient {
	return &mockMachineStatusClient{getFunc: func(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error) {
		return &vergeos.MachineStatus{AgentGuestInfo: &vergeos.GuestInfo{Network: ifaces}}, nil
	}}
}

func v4(addr string) []vergeos.GuestIPAddress {
	return []vergeos.GuestIPAddress{{Type: "ipv4", Address: addr}, {Type: "ipv6", Address: "fd00::5"}}
}

// docker0/cni0/flannel/cilium/bridge addresses reported by the agent must never
// become InternalIPs, even when listed first; only the VM-NIC-backed eth0 does.
func TestInstanceMetadata_AgentSkipsVirtualInterfaces(t *testing.T) {
	const mac = "52:54:00:00:00:01"
	i := &instances{
		vms: metaVM(),
		machineStatus: agentWith(
			vergeos.GuestNetworkInterface{Name: "lo", HardwareAddress: "00:00:00:00:00:00", IPAddresses: v4("127.0.0.1")},
			vergeos.GuestNetworkInterface{Name: "docker0", HardwareAddress: "02:42:ac:11:00:01", IPAddresses: v4("172.17.0.1")},
			vergeos.GuestNetworkInterface{Name: "cni0", HardwareAddress: "aa:aa:aa:aa:aa:01", IPAddresses: v4("10.244.0.1")},
			vergeos.GuestNetworkInterface{Name: "flannel.1", HardwareAddress: "aa:aa:aa:aa:aa:02", IPAddresses: v4("10.244.0.0")},
			vergeos.GuestNetworkInterface{Name: "cilium_host", HardwareAddress: "aa:aa:aa:aa:aa:03", IPAddresses: v4("10.0.0.1")},
			vergeos.GuestNetworkInterface{Name: "kube-bridge", HardwareAddress: "aa:aa:aa:aa:aa:04", IPAddresses: v4("10.99.0.1")},
			// Even if a virtual iface shares a NIC's MAC (bridge enslaving eth0), skip it.
			vergeos.GuestNetworkInterface{Name: "br0", HardwareAddress: "52:54:00:00:00:01", IPAddresses: v4("10.77.0.1")},
			vergeos.GuestNetworkInterface{Name: "eth0", HardwareAddress: "52:54:00:00:00:01", IPAddresses: append(v4("10.1.101.60"), vergeos.GuestIPAddress{Type: "ipv4", Address: "169.254.1.2"})},
		),
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			return []vergeos.VMNIC{{ID: 1, VNET: 10, MAC: mac}}, nil // no lease yet
		}},
	}
	meta, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	want := []v1.NodeAddress{
		{Type: v1.NodeInternalIP, Address: "10.1.101.60"},
		{Type: v1.NodeHostName, Address: "ns-machine"},
	}
	if !reflect.DeepEqual(meta.NodeAddresses, want) {
		t.Errorf("addresses = %v, want %v", meta.NodeAddresses, want)
	}
}

// Agent IPs on an interface whose MAC is not a VM NIC are ignored, and
// nodeNetworkID applies to agent IPs through the NIC's vnet.
func TestInstanceMetadata_AgentHonoursNodeNetworkID(t *testing.T) {
	i := &instances{
		vms:           metaVM(),
		nodeNetworkID: 10,
		machineStatus: agentWith(
			vergeos.GuestNetworkInterface{Name: "eth1", HardwareAddress: "52:54:00:00:00:02", IPAddresses: v4("10.9.0.5")},
			vergeos.GuestNetworkInterface{Name: "eth2", HardwareAddress: "de:ad:be:ef:00:01", IPAddresses: v4("192.168.5.5")},
			vergeos.GuestNetworkInterface{Name: "eth0", HardwareAddress: "52:54:00:00:00:01", IPAddresses: v4("10.1.101.60")},
		),
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			return []vergeos.VMNIC{
				{ID: 1, VNET: 10, MAC: "52:54:00:00:00:01"},
				{ID: 2, VNET: 20, MAC: "52:54:00:00:00:02"},
			}, nil
		}},
	}
	meta, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.NodeAddresses) != 2 || meta.NodeAddresses[0].Address != "10.1.101.60" {
		t.Errorf("expected only the vnet-10 NIC's agent IP, got %v", meta.NodeAddresses)
	}
}

// If the only agent IPs are on the wrong vnet, error instead of initializing without an IP.
func TestInstanceMetadata_AgentWrongNetworkOnlyIsError(t *testing.T) {
	i := &instances{
		vms:           metaVM(),
		nodeNetworkID: 10,
		machineStatus: agentWith(vergeos.GuestNetworkInterface{Name: "eth1", HardwareAddress: "52:54:00:00:00:02", IPAddresses: v4("10.9.0.5")}),
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			return []vergeos.VMNIC{{ID: 2, VNET: 20, MAC: "52:54:00:00:00:02"}}, nil
		}},
	}
	if _, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42")); err == nil {
		t.Error("expected error when no InternalIP is on nodeNetworkID")
	}
}

func TestIsVirtualIface(t *testing.T) {
	for _, n := range []string{"lo", "docker0", "br-1a2b", "br0", "virbr0", "cni0", "flannel.1", "cilium_host", "cilium_net", "cali123", "veth9f", "lxc_health", "tunl0", "kube-bridge", "vxlan.calico"} {
		if !isVirtualIface(n) {
			t.Errorf("%q should be virtual", n)
		}
	}
	for _, n := range []string{"eth0", "ens3", "enp1s0", "bond0", "eno1"} {
		if isVirtualIface(n) {
			t.Errorf("%q should not be virtual", n)
		}
	}
}

func TestInstanceMetadata_AgentErrorFallsBackToNICs(t *testing.T) {
	i := &instances{
		vms: metaVM(),
		machineStatus: &mockMachineStatusClient{getFunc: func(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error) {
			return nil, fmt.Errorf("boom")
		}},
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			return []vergeos.VMNIC{{ID: 1, IPAddress: "10.1.101.50"}}, nil
		}},
	}
	meta, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.NodeAddresses[0].Address != "10.1.101.50" {
		t.Errorf("got %v", meta.NodeAddresses)
	}
}

func TestInstanceMetadata_NICListError(t *testing.T) {
	i := &instances{
		vms:           metaVM(),
		machineStatus: noAgent(),
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			return nil, fmt.Errorf("api down")
		}},
	}
	if _, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42")); err == nil {
		t.Error("expected error so the controller retries instead of initializing without an IP")
	}
}

func TestInstanceMetadata_HostnameIsNodeName(t *testing.T) {
	i := &instances{vms: metaVM(), machineStatus: noAgent(), nics: leaseNICs()}
	meta, err := i.InstanceMetadata(context.Background(), testNode("kubelet-name", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	last := meta.NodeAddresses[len(meta.NodeAddresses)-1]
	if last != (v1.NodeAddress{Type: v1.NodeHostName, Address: "kubelet-name"}) {
		t.Errorf("hostname = %v, want node name (not vm.Name)", last)
	}
}

func TestInstanceMetadata_LegacyProviderIDEmitsUUID(t *testing.T) {
	i := &instances{vms: metaVM(), machineStatus: noAgent(), nics: leaseNICs()}
	meta, err := i.InstanceMetadata(context.Background(), testNode("n", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.ProviderID != "vergeos://"+testUUID {
		t.Errorf("providerID = %s", meta.ProviderID)
	}
}

func TestLegacyKeyMustBeDigits(t *testing.T) {
	for _, id := range []string{"+5", "-1", "0x10"} {
		if _, err := parseProviderID("vergeos://" + id); err == nil {
			t.Errorf("%q should be rejected", id)
		}
	}
}

func TestFindVM_RejectsQuote(t *testing.T) {
	f := &fakeVMs{}
	_, err := (&instances{vms: f}).getVM(context.Background(), testNode("a' or name ne 'b", ""))
	if err == nil || len(f.filters) != 0 {
		t.Errorf("expected rejection without API call, err=%v filters=%v", err, f.filters)
	}
}

// API failures (including 404 from the list endpoint) must never read as "VM gone".
func TestListFailure_IsErrorNotAbsent(t *testing.T) {
	failures := map[string]error{
		"404":   &vergeos.APIError{StatusCode: 404},
		"500":   &vergeos.APIError{StatusCode: 500},
		"plain": fmt.Errorf("boom"),
	}
	nodes := map[string]*v1.Node{
		"uuid":       testNode("n", "vergeos://"+testUUID),
		"name":       testNode("n", ""),
		"systemUUID": func() *v1.Node { n := testNode("n", ""); n.Status.NodeInfo.SystemUUID = testUUID; return n }(),
	}
	for fname, ferr := range failures {
		for nname, node := range nodes {
			i := &instances{vms: &mockVMClient{listFunc: func(ctx context.Context, opts ...vergeos.ListOption) ([]vergeos.VM, error) {
				return nil, ferr
			}}, machineStatus: noAgent()}
			t.Run(fname+"/"+nname, func(t *testing.T) {
				if ex, err := i.InstanceExists(context.Background(), node); err == nil || ex {
					t.Errorf("InstanceExists = %v, %v; want error", ex, err)
				}
				if sd, err := i.InstanceShutdown(context.Background(), node); err == nil || sd {
					t.Errorf("InstanceShutdown = %v, %v; want error", sd, err)
				}
				if m, err := i.InstanceMetadata(context.Background(), node); err == nil || m != nil {
					t.Errorf("InstanceMetadata = %v, %v; want error", m, err)
				}
			})
		}
	}
}

func TestGetByKey_NonNotFoundErrorPropagates(t *testing.T) {
	i := &instances{vms: &mockVMClient{getFunc: func(ctx context.Context, id int) (*vergeos.VM, error) {
		return nil, &vergeos.APIError{StatusCode: 500}
	}}}
	if ex, err := i.InstanceExists(context.Background(), testNode("n", "vergeos://42")); err == nil || ex {
		t.Errorf("got %v, %v; want error", ex, err)
	}
}

func TestInstanceShutdown_UUIDProviderID(t *testing.T) {
	f := &fakeVMs{byFilter: map[string][]vergeos.VM{
		uuidFilter(testUUID): {{ID: 7, UUID: testUUID, PowerState: false}},
	}}
	sd, err := (&instances{vms: f}).InstanceShutdown(context.Background(), testNode("n", "vergeos://"+testUUID))
	if err != nil || !sd {
		t.Errorf("got %v, %v; want true, nil", sd, err)
	}
	f.byFilter[uuidFilter(testUUID)][0].PowerState = true
	sd, err = (&instances{vms: f}).InstanceShutdown(context.Background(), testNode("n", "vergeos://"+testUUID))
	if err != nil || sd {
		t.Errorf("running: got %v, %v; want false, nil", sd, err)
	}
}
