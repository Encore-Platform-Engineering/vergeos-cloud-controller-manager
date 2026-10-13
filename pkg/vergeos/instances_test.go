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

func TestInstanceMetadata(t *testing.T) {
	i := &instances{
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

func TestInstanceMetadata_GuestAgentWinsOverNICs(t *testing.T) {
	i := &instances{
		vms: metaVM(),
		machineStatus: &mockMachineStatusClient{getFunc: func(ctx context.Context, machineKey int) (*vergeos.MachineStatus, error) {
			return &vergeos.MachineStatus{AgentGuestInfo: &vergeos.GuestInfo{
				Network: []vergeos.GuestNetworkInterface{{
					Name:        "ens1",
					IPAddresses: []vergeos.GuestIPAddress{{Type: "ipv4", Address: "10.0.0.5"}},
				}},
			}}, nil
		}},
		nics: &mockNICClient{list: func(ctx context.Context, machineID int) ([]vergeos.VMNIC, error) {
			t.Error("NIC leases must not be consulted when the guest agent reports IPs")
			return nil, nil
		}},
	}
	meta, err := i.InstanceMetadata(context.Background(), testNode("ns-machine", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.NodeAddresses) != 2 || meta.NodeAddresses[0].Address != "10.0.0.5" {
		t.Errorf("got %v", meta.NodeAddresses)
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
	i := &instances{vms: metaVM(), machineStatus: noAgent()}
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
	i := &instances{vms: metaVM(), machineStatus: noAgent()}
	meta, err := i.InstanceMetadata(context.Background(), testNode("n", "vergeos://42"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.ProviderID != "vergeos://"+testUUID {
		t.Errorf("providerID = %s", meta.ProviderID)
	}
}
