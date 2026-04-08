package vergeos

import (
	"context"
	"fmt"
	"testing"

	vergeos "github.com/verge-io/govergeos"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Mock VNetRuleClient for testing.
type mockVNetRuleClient struct {
	listByNetworkFunc func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error)
	getByNameFunc     func(ctx context.Context, vnetID int, name string) (*vergeos.VNetRule, error)
	createFunc        func(ctx context.Context, req *vergeos.VNetRuleCreateRequest) (*vergeos.VNetRule, error)
	updateFunc        func(ctx context.Context, id int, req *vergeos.VNetRuleUpdateRequest) (*vergeos.VNetRule, error)
	deleteFunc        func(ctx context.Context, id int) error
}

func (m *mockVNetRuleClient) ListByNetwork(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
	return m.listByNetworkFunc(ctx, vnetID, opts...)
}
func (m *mockVNetRuleClient) GetByName(ctx context.Context, vnetID int, name string) (*vergeos.VNetRule, error) {
	return m.getByNameFunc(ctx, vnetID, name)
}
func (m *mockVNetRuleClient) Create(ctx context.Context, req *vergeos.VNetRuleCreateRequest) (*vergeos.VNetRule, error) {
	return m.createFunc(ctx, req)
}
func (m *mockVNetRuleClient) Update(ctx context.Context, id int, req *vergeos.VNetRuleUpdateRequest) (*vergeos.VNetRule, error) {
	return m.updateFunc(ctx, id, req)
}
func (m *mockVNetRuleClient) Delete(ctx context.Context, id int) error {
	return m.deleteFunc(ctx, id)
}

// Mock NetworkClient for testing.
type mockNetworkClient struct {
	getFunc        func(ctx context.Context, id int) (*vergeos.Network, error)
	applyRulesFunc func(ctx context.Context, id int) error
}

func (m *mockNetworkClient) Get(ctx context.Context, id int) (*vergeos.Network, error) {
	return m.getFunc(ctx, id)
}
func (m *mockNetworkClient) ApplyRules(ctx context.Context, id int) error {
	return m.applyRulesFunc(ctx, id)
}

// testService creates a v1.Service for testing.
func testService(namespace, name string, ports []v1.ServicePort) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1.ServiceSpec{
			Ports: ports,
			Type:  v1.ServiceTypeLoadBalancer,
		},
	}
}

// testNodes creates nodes with InternalIP addresses.
func testNodes(ips ...string) []*v1.Node {
	var nodes []*v1.Node
	for i, ip := range ips {
		nodes = append(nodes, &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%d", i)},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{Type: v1.NodeInternalIP, Address: ip},
				},
			},
		})
	}
	return nodes
}

func TestLBRuleName(t *testing.T) {
	tests := []struct {
		namespace   string
		serviceName string
		port        int32
		protocol    string
		want        string
	}{
		{"default", "nginx", 80, "tcp", "k8s-default-nginx-tcp-80"},
		{"kube-system", "api", 443, "tcp", "k8s-kube-system-api-tcp-443"},
		{"prod", "app", 8080, "udp", "k8s-prod-app-udp-8080"},
	}
	for _, tt := range tests {
		got := lbRuleName(tt.namespace, tt.serviceName, v1.Protocol(tt.protocol), tt.port)
		if got != tt.want {
			t.Errorf("lbRuleName(%q, %q, %q, %d) = %q, want %q",
				tt.namespace, tt.serviceName, tt.protocol, tt.port, got, tt.want)
		}
	}
}

func TestLBRulePrefix(t *testing.T) {
	got := lbRulePrefix("default", "nginx")
	want := "k8s-default-nginx-"
	if got != want {
		t.Errorf("lbRulePrefix() = %q, want %q", got, want)
	}
}

func TestGetLoadBalancerName(t *testing.T) {
	lb := &loadBalancer{}
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-svc",
			Namespace: "default",
		},
	}
	got := lb.GetLoadBalancerName(nil, "cluster-1", svc)
	want := "k8s-default-my-svc"
	if got != want {
		t.Errorf("GetLoadBalancerName() = %q, want %q", got, want)
	}
}

func TestNodeAddresses(t *testing.T) {
	nodes := testNodes("10.0.1.1", "10.0.1.2", "10.0.1.3")
	addrs := nodeAddresses(nodes)
	if len(addrs) != 3 {
		t.Fatalf("expected 3 addresses, got %d", len(addrs))
	}
	if addrs[0] != "10.0.1.1" {
		t.Errorf("expected first addr=10.0.1.1, got %s", addrs[0])
	}
}

func TestProtocolToVNetProtocol(t *testing.T) {
	tests := []struct {
		input v1.Protocol
		want  string
	}{
		{v1.ProtocolTCP, "tcp"},
		{v1.ProtocolUDP, "udp"},
		{v1.Protocol("SCTP"), "tcp"}, // Unsupported defaults to tcp
	}
	for _, tt := range tests {
		got := protocolToVNetProtocol(tt.input)
		if got != tt.want {
			t.Errorf("protocolToVNetProtocol(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAllocateIP_FirstAvailable(t *testing.T) {
	lb := &loadBalancer{
		config: lbConfig{
			NetworkID: 5,
			IPPool:    []string{"10.0.0.100", "10.0.0.101", "10.0.0.102"},
		},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				// First IP is in use by another k8s service
				return []vergeos.VNetRule{
					{Name: "k8s-default-existing-tcp-80", DestinationIP: "10.0.0.100"},
				}, nil
			},
		},
	}

	ip, err := lb.allocateIP(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != "10.0.0.101" {
		t.Errorf("expected IP=10.0.0.101 (first unused), got %s", ip)
	}
}

func TestAllocateIP_PoolExhausted(t *testing.T) {
	lb := &loadBalancer{
		config: lbConfig{
			NetworkID: 5,
			IPPool:    []string{"10.0.0.100"},
		},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{
					{Name: "k8s-default-existing-tcp-80", DestinationIP: "10.0.0.100"},
				}, nil
			},
		},
	}

	_, err := lb.allocateIP(context.Background())
	if err == nil {
		t.Error("expected pool exhausted error")
	}
}

func TestFindServiceRules(t *testing.T) {
	lb := &loadBalancer{
		config: lbConfig{NetworkID: 5},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{
					{Name: "k8s-default-nginx-tcp-80", DestinationIP: "10.0.0.100"},
					{Name: "k8s-default-nginx-tcp-443", DestinationIP: "10.0.0.100"},
					{Name: "k8s-other-svc-tcp-80", DestinationIP: "10.0.0.101"},
					{Name: "unrelated-rule"},
				}, nil
			},
		},
	}

	rules, err := lb.findServiceRules(context.Background(), "default", "nginx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 2 {
		t.Errorf("expected 2 matching rules, got %d", len(rules))
	}
}

func TestGetLoadBalancer_Exists(t *testing.T) {
	lb := &loadBalancer{
		config: lbConfig{NetworkID: 5, IPPool: []string{"10.0.0.100"}},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{
					{Name: "k8s-default-nginx-tcp-80", DestinationIP: "10.0.0.100"},
				}, nil
			},
		},
	}

	svc := testService("default", "nginx", []v1.ServicePort{{Port: 80, Protocol: v1.ProtocolTCP}})
	status, exists, err := lb.GetLoadBalancer(context.Background(), "cluster", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected LB to exist")
	}
	if len(status.Ingress) != 1 || status.Ingress[0].IP != "10.0.0.100" {
		t.Errorf("expected ingress IP=10.0.0.100, got %v", status.Ingress)
	}
}

func TestGetLoadBalancer_NotExists(t *testing.T) {
	lb := &loadBalancer{
		config: lbConfig{NetworkID: 5},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{}, nil
			},
		},
	}

	svc := testService("default", "nginx", nil)
	_, exists, err := lb.GetLoadBalancer(context.Background(), "cluster", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected LB to not exist")
	}
}

func TestEnsureLoadBalancer_Create(t *testing.T) {
	var createdRules []*vergeos.VNetRuleCreateRequest
	var appliedNetworkID int

	lb := &loadBalancer{
		config: lbConfig{
			NetworkID: 5,
			IPPool:    []string{"10.0.0.100", "10.0.0.101"},
		},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{}, nil // No existing rules.
			},
			createFunc: func(ctx context.Context, req *vergeos.VNetRuleCreateRequest) (*vergeos.VNetRule, error) {
				createdRules = append(createdRules, req)
				return &vergeos.VNetRule{Key: 1}, nil
			},
		},
		network: &mockNetworkClient{
			applyRulesFunc: func(ctx context.Context, id int) error {
				appliedNetworkID = id
				return nil
			},
		},
	}

	svc := testService("default", "nginx", []v1.ServicePort{
		{Port: 80, NodePort: 30080, Protocol: v1.ProtocolTCP},
	})
	nodes := testNodes("10.0.1.1", "10.0.1.2")

	status, err := lb.EnsureLoadBalancer(context.Background(), "cluster", svc, nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(status.Ingress) != 1 || status.Ingress[0].IP != "10.0.0.100" {
		t.Errorf("expected ingress IP=10.0.0.100, got %v", status.Ingress)
	}
	if len(createdRules) != 1 {
		t.Fatalf("expected 1 rule created, got %d", len(createdRules))
	}
	if createdRules[0].Name != "k8s-default-nginx-tcp-80" {
		t.Errorf("expected rule name=k8s-default-nginx-tcp-80, got %s", createdRules[0].Name)
	}
	if appliedNetworkID != 5 {
		t.Errorf("expected ApplyRules on network 5, got %d", appliedNetworkID)
	}
}

func TestEnsureLoadBalancer_MultiPort(t *testing.T) {
	var createdRules []*vergeos.VNetRuleCreateRequest

	lb := &loadBalancer{
		config: lbConfig{
			NetworkID: 5,
			IPPool:    []string{"10.0.0.100"},
		},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{}, nil
			},
			createFunc: func(ctx context.Context, req *vergeos.VNetRuleCreateRequest) (*vergeos.VNetRule, error) {
				createdRules = append(createdRules, req)
				return &vergeos.VNetRule{Key: 1}, nil
			},
		},
		network: &mockNetworkClient{
			applyRulesFunc: func(ctx context.Context, id int) error { return nil },
		},
	}

	svc := testService("default", "web", []v1.ServicePort{
		{Port: 80, NodePort: 30080, Protocol: v1.ProtocolTCP},
		{Port: 443, NodePort: 30443, Protocol: v1.ProtocolTCP},
	})
	nodes := testNodes("10.0.1.1")

	_, err := lb.EnsureLoadBalancer(context.Background(), "cluster", svc, nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(createdRules) != 2 {
		t.Fatalf("expected 2 rules created (one per port), got %d", len(createdRules))
	}
	if createdRules[0].Name != "k8s-default-web-tcp-80" {
		t.Errorf("expected first rule name=k8s-default-web-tcp-80, got %s", createdRules[0].Name)
	}
	if createdRules[1].Name != "k8s-default-web-tcp-443" {
		t.Errorf("expected second rule name=k8s-default-web-tcp-443, got %s", createdRules[1].Name)
	}
}

func TestEnsureLoadBalancer_AlreadyExists(t *testing.T) {
	var createdRules []*vergeos.VNetRuleCreateRequest

	lb := &loadBalancer{
		config: lbConfig{
			NetworkID: 5,
			IPPool:    []string{"10.0.0.100"},
		},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				// LB already exists
				return []vergeos.VNetRule{
					{Name: "k8s-default-nginx-tcp-80", DestinationIP: "10.0.0.100"},
				}, nil
			},
			createFunc: func(ctx context.Context, req *vergeos.VNetRuleCreateRequest) (*vergeos.VNetRule, error) {
				createdRules = append(createdRules, req)
				return &vergeos.VNetRule{Key: 1}, nil
			},
		},
	}

	svc := testService("default", "nginx", []v1.ServicePort{
		{Port: 80, NodePort: 30080, Protocol: v1.ProtocolTCP},
	})
	nodes := testNodes("10.0.1.1")

	status, err := lb.EnsureLoadBalancer(context.Background(), "cluster", svc, nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(status.Ingress) != 1 || status.Ingress[0].IP != "10.0.0.100" {
		t.Errorf("expected ingress IP=10.0.0.100, got %v", status.Ingress)
	}
	// Should NOT create new rules (idempotent)
	if len(createdRules) != 0 {
		t.Errorf("expected 0 rules created (already exists), got %d", len(createdRules))
	}
}

func TestUpdateLoadBalancer(t *testing.T) {
	var updatedIDs []int
	var updatedTargetIPs []string

	lb := &loadBalancer{
		config: lbConfig{NetworkID: 5},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{
					{Key: 10, Name: "k8s-default-nginx-tcp-80", DestinationIP: "10.0.0.100"},
					{Key: 11, Name: "k8s-default-nginx-tcp-443", DestinationIP: "10.0.0.100"},
				}, nil
			},
			updateFunc: func(ctx context.Context, id int, req *vergeos.VNetRuleUpdateRequest) (*vergeos.VNetRule, error) {
				updatedIDs = append(updatedIDs, id)
				if req.TargetIP != nil {
					updatedTargetIPs = append(updatedTargetIPs, *req.TargetIP)
				}
				return &vergeos.VNetRule{Key: vergeos.FlexInt(id)}, nil
			},
		},
		network: &mockNetworkClient{
			applyRulesFunc: func(ctx context.Context, id int) error { return nil },
		},
	}

	svc := testService("default", "nginx", nil)
	// New node set (node-2 replaced node-1)
	nodes := testNodes("10.0.1.2", "10.0.1.3")

	err := lb.UpdateLoadBalancer(context.Background(), "cluster", svc, nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updatedIDs) != 2 {
		t.Fatalf("expected 2 rules updated, got %d", len(updatedIDs))
	}
	// Both rules should get the same new target
	expectedTarget := "10.0.1.2,10.0.1.3"
	for i, target := range updatedTargetIPs {
		if target != expectedTarget {
			t.Errorf("rule %d: expected target=%s, got %s", updatedIDs[i], expectedTarget, target)
		}
	}
}

func TestUpdateLoadBalancer_NoRules(t *testing.T) {
	lb := &loadBalancer{
		config: lbConfig{NetworkID: 5},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{}, nil
			},
		},
	}

	svc := testService("default", "nginx", nil)
	nodes := testNodes("10.0.1.1")

	err := lb.UpdateLoadBalancer(context.Background(), "cluster", svc, nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No error, nothing to update.
}

func TestEnsureLoadBalancerDeleted(t *testing.T) {
	var deletedIDs []int

	lb := &loadBalancer{
		config: lbConfig{NetworkID: 5},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{
					{Key: 10, Name: "k8s-default-nginx-tcp-80"},
					{Key: 11, Name: "k8s-default-nginx-tcp-443"},
					{Key: 99, Name: "other-rule"}, // Not ours.
				}, nil
			},
			deleteFunc: func(ctx context.Context, id int) error {
				deletedIDs = append(deletedIDs, id)
				return nil
			},
		},
		network: &mockNetworkClient{
			applyRulesFunc: func(ctx context.Context, id int) error { return nil },
		},
	}

	svc := testService("default", "nginx", nil)
	err := lb.EnsureLoadBalancerDeleted(context.Background(), "cluster", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deletedIDs) != 2 {
		t.Errorf("expected 2 rules deleted, got %d: %v", len(deletedIDs), deletedIDs)
	}
}

func TestEnsureLoadBalancerDeleted_NoRules(t *testing.T) {
	lb := &loadBalancer{
		config: lbConfig{NetworkID: 5},
		rules: &mockVNetRuleClient{
			listByNetworkFunc: func(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
				return []vergeos.VNetRule{}, nil
			},
		},
		network: &mockNetworkClient{
			applyRulesFunc: func(ctx context.Context, id int) error { return nil },
		},
	}

	svc := testService("default", "nginx", nil)
	err := lb.EnsureLoadBalancerDeleted(context.Background(), "cluster", svc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No error, idempotent.
}
