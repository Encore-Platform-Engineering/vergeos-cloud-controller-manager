package vergeos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	vergeos "github.com/verge-io/govergeos"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// fakeVNet is a stateful in-memory VergeOS vnet implementing the rule,
// address and network client interfaces, with failure injection.
type fakeVNet struct {
	mu      sync.Mutex
	rules   []vergeos.VNetRule
	addrs   []vergeos.VNetAddress
	nextKey int

	applies, creates, updates, deletes int
	addrLists, addrGets                int

	failCreate func(name string) error
	failDelete func(name string) error
	failApply  error
	needFW     bool
}

func (f *fakeVNet) key() vergeos.FlexInt { f.nextKey++; return vergeos.FlexInt(f.nextKey) }

type fakeRules struct{ f *fakeVNet }
type fakeAddrs struct{ f *fakeVNet }
type fakeNet struct{ f *fakeVNet }

func (r fakeRules) ListByNetwork(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetRule, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return append([]vergeos.VNetRule{}, r.f.rules...), nil
}
func (r fakeRules) GetByName(ctx context.Context, vnetID int, name string) (*vergeos.VNetRule, error) {
	return nil, errors.New("unused")
}
func (r fakeRules) Create(ctx context.Context, req *vergeos.VNetRuleCreateRequest) (*vergeos.VNetRule, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if r.f.failCreate != nil {
		if err := r.f.failCreate(req.Name); err != nil {
			return nil, err
		}
	}
	for _, x := range r.f.rules {
		if x.Name == req.Name {
			return nil, fmt.Errorf("duplicate rule name %q", req.Name)
		}
	}
	rule := vergeos.VNetRule{
		Key: r.f.key(), VNet: vergeos.FlexInt(req.VNet), Name: req.Name, Description: req.Description,
		Enabled: *req.Enabled, Protocol: *req.Protocol, Direction: *req.Direction, Action: *req.Action,
		DestinationIP: *req.DestinationIP, DestinationPorts: *req.DestinationPorts,
	}
	if req.TargetIP != nil {
		rule.TargetIP = *req.TargetIP
	}
	if req.TargetPorts != nil {
		rule.TargetPorts = *req.TargetPorts
	}
	if req.SourceIP != nil {
		rule.SourceIP = *req.SourceIP
	}
	r.f.rules = append(r.f.rules, rule)
	r.f.creates++
	return &rule, nil
}
func (r fakeRules) Update(ctx context.Context, id int, req *vergeos.VNetRuleUpdateRequest) (*vergeos.VNetRule, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	for i := range r.f.rules {
		x := &r.f.rules[i]
		if int(x.Key) != id {
			continue
		}
		if req.Name != nil {
			x.Name = *req.Name
		}
		if req.Description != nil {
			x.Description = *req.Description
		}
		if req.Enabled != nil {
			x.Enabled = *req.Enabled
		}
		if req.Protocol != nil {
			x.Protocol = *req.Protocol
		}
		if req.Direction != nil {
			x.Direction = *req.Direction
		}
		if req.Action != nil {
			x.Action = *req.Action
		}
		if req.DestinationIP != nil {
			x.DestinationIP = *req.DestinationIP
		}
		if req.DestinationPorts != nil {
			x.DestinationPorts = *req.DestinationPorts
		}
		if req.TargetIP != nil {
			x.TargetIP = *req.TargetIP
		}
		if req.TargetPorts != nil {
			x.TargetPorts = *req.TargetPorts
		}
		if req.SourceIP != nil {
			x.SourceIP = *req.SourceIP
		}
		r.f.updates++
		cp := *x
		return &cp, nil
	}
	return nil, &vergeos.NotFoundError{Resource: "vnet_rules", ID: id}
}
func (r fakeRules) Delete(ctx context.Context, id int) error {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	for i, x := range r.f.rules {
		if int(x.Key) != id {
			continue
		}
		if r.f.failDelete != nil {
			if err := r.f.failDelete(x.Name); err != nil {
				return err
			}
		}
		r.f.rules = append(r.f.rules[:i], r.f.rules[i+1:]...)
		r.f.deletes++
		return nil
	}
	return &vergeos.NotFoundError{Resource: "vnet_rules", ID: id}
}

func (a fakeAddrs) ListByNetwork(ctx context.Context, vnetID int, opts ...vergeos.ListOption) ([]vergeos.VNetAddress, error) {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	a.f.addrLists++
	return append([]vergeos.VNetAddress{}, a.f.addrs...), nil
}
func (a fakeAddrs) GetByIP(ctx context.Context, vnetID int, ip string) (*vergeos.VNetAddress, error) {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	a.f.addrGets++
	for _, x := range a.f.addrs {
		if x.IP == ip {
			cp := x
			return &cp, nil
		}
	}
	return nil, &vergeos.NotFoundError{Resource: "vnet_addresses", ID: ip}
}
func (a fakeAddrs) Create(ctx context.Context, req *vergeos.VNetAddressCreateRequest) (*vergeos.VNetAddress, error) {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	for _, x := range a.f.addrs {
		if x.IP == req.IP {
			return nil, fmt.Errorf("duplicate address %s", req.IP)
		}
	}
	x := vergeos.VNetAddress{Key: a.f.key(), VNet: vergeos.FlexInt(req.VNet), IP: req.IP, Type: req.Type, Hostname: req.Hostname, Description: req.Description}
	a.f.addrs = append(a.f.addrs, x)
	return &x, nil
}
func (a fakeAddrs) Update(ctx context.Context, id int, req *vergeos.VNetAddressUpdateRequest) (*vergeos.VNetAddress, error) {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	for i := range a.f.addrs {
		x := &a.f.addrs[i]
		if int(x.Key) != id {
			continue
		}
		if req.Hostname != nil {
			x.Hostname = *req.Hostname
		}
		if req.Description != nil {
			x.Description = *req.Description
		}
		cp := *x
		return &cp, nil
	}
	return nil, &vergeos.NotFoundError{Resource: "vnet_addresses", ID: id}
}
func (a fakeAddrs) Delete(ctx context.Context, id int) error {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	for i, x := range a.f.addrs {
		if int(x.Key) == id {
			a.f.addrs = append(a.f.addrs[:i], a.f.addrs[i+1:]...)
			return nil
		}
	}
	return &vergeos.NotFoundError{Resource: "vnet_addresses", ID: id}
}

func (n fakeNet) Get(ctx context.Context, id int) (*vergeos.Network, error) {
	n.f.mu.Lock()
	defer n.f.mu.Unlock()
	return &vergeos.Network{NeedFWApply: n.f.needFW}, nil
}
func (n fakeNet) ApplyRules(ctx context.Context, id int) error {
	n.f.mu.Lock()
	defer n.f.mu.Unlock()
	if n.f.failApply != nil {
		return n.f.failApply
	}
	n.f.applies++
	n.f.needFW = false
	return nil
}

func newTestLB(pool ...string) (*loadBalancer, *fakeVNet) {
	f := &fakeVNet{}
	lb := newLoadBalancer(fakeRules{f}, fakeNet{f}, fakeAddrs{f}, lbConfig{Enabled: true, NetworkID: 5, IPPool: pool})
	return lb, f
}

func testService(namespace, name string, ports []v1.ServicePort) *v1.Service {
	return &v1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID("uid-" + namespace + "-" + name)},
		Spec:       v1.ServiceSpec{Ports: ports, Type: v1.ServiceTypeLoadBalancer},
	}
}

func tcp(port, nodePort int32) v1.ServicePort {
	return v1.ServicePort{Port: port, NodePort: nodePort, Protocol: v1.ProtocolTCP}
}

func testNodes(ips ...string) []*v1.Node {
	var nodes []*v1.Node
	for i, ip := range ips {
		nodes = append(nodes, &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("node-%d", i)},
			Status:     v1.NodeStatus{Addresses: []v1.NodeAddress{{Type: v1.NodeInternalIP, Address: ip}}},
		})
	}
	return nodes
}

func mustEnsure(t *testing.T, lb *loadBalancer, cluster string, svc *v1.Service, nodes []*v1.Node) string {
	t.Helper()
	st, err := lb.EnsureLoadBalancer(context.Background(), cluster, svc, nodes)
	if err != nil {
		t.Fatalf("EnsureLoadBalancer(%s/%s): %v", svc.Namespace, svc.Name, err)
	}
	if len(st.Ingress) != 1 {
		t.Fatalf("expected 1 ingress, got %v", st.Ingress)
	}
	return st.Ingress[0].IP
}

func (f *fakeVNet) ruleFor(t *testing.T, destPort string, destIP string) vergeos.VNetRule {
	t.Helper()
	for _, r := range f.rules {
		if r.DestinationPorts == destPort && r.DestinationIP == destIP {
			return r
		}
	}
	t.Fatalf("no rule for %s:%s in %+v", destIP, destPort, f.rules)
	return vergeos.VNetRule{}
}

func (f *fakeVNet) hasAddr(ip string) bool {
	for _, a := range f.addrs {
		if a.IP == ip {
			return true
		}
	}
	return false
}

// --- ownership encoding ---

func TestOwnerStringsNeverCollide(t *testing.T) {
	owners := []lbOwner{
		{"c", "a-b", "c", "u"},
		{"c", "a", "b-c", "u"},
		{"c", "default", "web", "u"},
		{"c", "default", "web-api", "u"},
		{"c2", "default", "web", "u"},
		{"c", "default", "web", "u2"},
		// Hostile values that would collide under naive joining.
		{"c namespace=default", "web", "x", "u"},
		{"c", "default name=web", "x", "u"},
		{"c", "default", "web uid=u", ""},
		{"c", "default", "web", "u uid=u2"},
	}
	seen := map[string]lbOwner{}
	ids := map[string]lbOwner{}
	for _, o := range owners {
		if prev, ok := seen[o.String()]; ok {
			t.Errorf("owner strings collide: %+v and %+v -> %q", prev, o, o.String())
		}
		seen[o.String()] = o
		if prev, ok := ids[o.id()]; ok {
			t.Errorf("owner ids collide: %+v and %+v", prev, o)
		}
		ids[o.id()] = o
	}
}

func TestRuleNameShape(t *testing.T) {
	o := lbOwner{"c", "default", "web", "u"}
	name := o.ruleName("tcp", 443)
	if !strings.HasPrefix(name, "k8s-") || !strings.HasSuffix(name, "-tcp-443") {
		t.Errorf("unexpected rule name %q", name)
	}
	if o.ruleName("tcp", 443) == o.ruleName("udp", 443) || o.ruleName("tcp", 443) == o.ruleName("tcp", 80) {
		t.Error("rule names must differ by protocol and port")
	}
}

func TestGetLoadBalancerName(t *testing.T) {
	lb := &loadBalancer{}
	svc := &v1.Service{ObjectMeta: metav1.ObjectMeta{Name: "my-svc", Namespace: "default"}}
	if got := lb.GetLoadBalancerName(context.Background(), "cluster-1", svc); got != "k8s-default-my-svc" {
		t.Errorf("GetLoadBalancerName() = %q", got)
	}
}

func TestNodeAddresses(t *testing.T) {
	addrs := nodeAddresses(testNodes("10.0.1.1", "10.0.1.2", "10.0.1.3"))
	if len(addrs) != 3 || addrs[0] != "10.0.1.1" {
		t.Errorf("unexpected addrs %v", addrs)
	}
}

func TestSortedTargets(t *testing.T) {
	got := sortedTargets(testNodes("10.0.1.3", "10.0.1.1", "10.0.1.3"))
	if strings.Join(got, ",") != "10.0.1.1,10.0.1.3" {
		t.Errorf("got %v", got)
	}
}

func TestProtocolToVNetProtocol(t *testing.T) {
	for in, want := range map[v1.Protocol]string{v1.ProtocolTCP: "tcp", v1.ProtocolUDP: "udp", v1.Protocol("SCTP"): "tcp"} {
		if got := protocolToVNetProtocol(in); got != want {
			t.Errorf("protocolToVNetProtocol(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- create / idempotency ---

func TestEnsure_CreatesRulesAndAlias(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "nginx", []v1.ServicePort{tcp(80, 30080), {Port: 53, NodePort: 30053, Protocol: v1.ProtocolUDP}})

	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.2", "10.0.1.1"))
	if ip != "10.0.0.100" {
		t.Fatalf("ip = %s, want first pool IP", ip)
	}
	if len(f.rules) != 2 || f.applies != 1 {
		t.Fatalf("rules=%d applies=%d, want 2 rules and a single apply", len(f.rules), f.applies)
	}
	r := f.ruleFor(t, "80", ip)
	owner := newLBOwner("c", svc)
	if r.Name != owner.ruleName("tcp", 80) || r.Description != owner.String() || r.Protocol != "tcp" ||
		r.Direction != "incoming" || r.Action != "translate" || !r.Enabled ||
		r.TargetIP != "10.0.1.1,10.0.1.2" || r.TargetPorts != "30080" {
		t.Errorf("unexpected rule %+v", r)
	}
	if u := f.ruleFor(t, "53", ip); u.Protocol != "udp" || u.TargetPorts != "30053" {
		t.Errorf("unexpected udp rule %+v", u)
	}
	if len(f.addrs) != 1 || f.addrs[0].IP != ip || f.addrs[0].Type != "ipalias" || f.addrs[0].Description != owner.String() {
		t.Errorf("unexpected addrs %+v", f.addrs)
	}
}

func TestEnsure_Idempotent(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "nginx", []v1.ServicePort{tcp(80, 30080)})
	nodes := testNodes("10.0.1.1", "10.0.1.2")
	ip := mustEnsure(t, lb, "c", svc, nodes)
	creates, updates, deletes, applies := f.creates, f.updates, f.deletes, f.applies

	// Same input, different node order: no writes and no apply.
	if ip2 := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.2", "10.0.1.1")); ip2 != ip {
		t.Fatalf("ip changed %s -> %s", ip, ip2)
	}
	if f.creates != creates || f.updates != updates || f.deletes != deletes || f.applies != applies {
		t.Errorf("second ensure wrote: creates %d->%d updates %d->%d deletes %d->%d applies %d->%d",
			creates, f.creates, updates, f.updates, deletes, f.deletes, applies, f.applies)
	}
}

func TestEnsure_ErrorsWithoutNodesOrNodePort(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "nginx", []v1.ServicePort{tcp(80, 30080)})
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, nil); err == nil {
		t.Error("expected error with no node addresses")
	}
	noNP := testService("default", "nginx", []v1.ServicePort{tcp(80, 0)})
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", noNP, testNodes("10.0.1.1")); err == nil {
		t.Error("expected error for port without NodePort")
	}
	sctp := testService("default", "nginx", []v1.ServicePort{{Port: 80, NodePort: 30080, Protocol: v1.ProtocolSCTP}})
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", sctp, testNodes("10.0.1.1")); err == nil {
		t.Error("expected error for SCTP")
	}
	if len(f.rules) != 0 || len(f.addrs) != 0 {
		t.Errorf("failed validation must not create anything: rules=%d addrs=%d", len(f.rules), len(f.addrs))
	}
}

func TestEnsure_PoolExhausted(t *testing.T) {
	lb, _ := newTestLB("10.0.0.100")
	mustEnsure(t, lb, "c", testService("default", "a", []v1.ServicePort{tcp(80, 30080)}), testNodes("10.0.1.1"))
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", testService("default", "b", []v1.ServicePort{tcp(80, 30081)}), testNodes("10.0.1.1")); err == nil {
		t.Error("expected pool exhausted error")
	}
}

// --- isolation ---

func TestIsolation_PrefixServices(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	web := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	api := testService("default", "web-api", []v1.ServicePort{tcp(443, 30443)})
	nodes := testNodes("10.0.1.1")
	webIP := mustEnsure(t, lb, "c", web, nodes)
	apiIP := mustEnsure(t, lb, "c", api, nodes)
	if webIP == apiIP {
		t.Fatalf("services share an IP: %s", webIP)
	}

	st, ok, err := lb.GetLoadBalancer(context.Background(), "c", web)
	if err != nil || !ok || st.Ingress[0].IP != webIP {
		t.Fatalf("GetLoadBalancer(web) = %v %v %v", st, ok, err)
	}

	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", web); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 1 || f.rules[0].DestinationIP != apiIP {
		t.Errorf("deleting web must leave web-api rules, got %+v", f.rules)
	}
	if f.hasAddr(webIP) || !f.hasAddr(apiIP) {
		t.Errorf("deleting web must remove only its ipalias: %+v", f.addrs)
	}
	// Also stays isolated after web-api is updated.
	if err := lb.UpdateLoadBalancer(context.Background(), "c", api, testNodes("10.0.1.9")); err != nil {
		t.Fatal(err)
	}
	if f.rules[0].TargetIP != "10.0.1.9" {
		t.Errorf("web-api not updated: %+v", f.rules[0])
	}
}

func TestIsolation_DashedNamespaces(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	// Both produce the legacy name prefix "k8s-a-b-c-".
	s1 := testService("a-b", "c", []v1.ServicePort{tcp(80, 30080)})
	s2 := testService("a", "b-c", []v1.ServicePort{tcp(80, 30081)})
	nodes := testNodes("10.0.1.1")
	ip1 := mustEnsure(t, lb, "c", s1, nodes)
	ip2 := mustEnsure(t, lb, "c", s2, nodes)
	if ip1 == ip2 || len(f.rules) != 2 {
		t.Fatalf("services must get separate LBs: %s %s rules=%d", ip1, ip2, len(f.rules))
	}
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", s1); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 1 || f.rules[0].DestinationIP != ip2 || f.rules[0].TargetPorts != "30081" {
		t.Errorf("deleting a-b/c touched a/b-c: %+v", f.rules)
	}
}

func TestIsolation_ClusterNames(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	a := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	b := testService("default", "web", []v1.ServicePort{tcp(80, 31080)})
	a.UID, b.UID = "", "" // same ns/name, no UID help: only clusterName differs
	ipA := mustEnsure(t, lb, "cluster-a", a, testNodes("10.0.1.1"))
	ipB := mustEnsure(t, lb, "cluster-b", b, testNodes("10.0.2.1"))
	if ipA == ipB {
		t.Fatalf("clusters share LB IP %s", ipA)
	}
	if got := f.ruleFor(t, "80", ipA); got.TargetPorts != "30080" || got.TargetIP != "10.0.1.1" {
		t.Errorf("cluster-a rule clobbered: %+v", got)
	}
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "cluster-a", a); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := lb.GetLoadBalancer(context.Background(), "cluster-b", b); !ok {
		t.Error("deleting cluster-a's LB removed cluster-b's")
	}
	if _, ok, _ := lb.GetLoadBalancer(context.Background(), "cluster-a", a); ok {
		t.Error("cluster-a LB should be gone")
	}
	if len(f.rules) != 1 || !f.hasAddr(ipB) {
		t.Errorf("unexpected leftovers: rules=%+v addrs=%+v", f.rules, f.addrs)
	}
}

func TestIsolation_SameNameDifferentUID(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	old := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	mustEnsure(t, lb, "c", old, testNodes("10.0.1.1"))
	recreated := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	recreated.UID = "different"
	if _, ok, _ := lb.GetLoadBalancer(context.Background(), "c", recreated); ok {
		t.Error("a recreated Service must not claim the old Service's rules")
	}
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", recreated); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 1 {
		t.Errorf("rules of the old UID must survive, got %+v", f.rules)
	}
}

// --- reconcile ---

func TestEnsure_AddRemoveAndChangePorts(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	nodes := testNodes("10.0.1.1")
	ip := mustEnsure(t, lb, "c", svc, nodes)

	// Add a port.
	svc.Spec.Ports = []v1.ServicePort{tcp(80, 30080), tcp(443, 30443)}
	mustEnsure(t, lb, "c", svc, nodes)
	if len(f.rules) != 2 {
		t.Fatalf("add port: rules=%+v", f.rules)
	}
	f.ruleFor(t, "443", ip)

	// Change a NodePort.
	svc.Spec.Ports = []v1.ServicePort{tcp(80, 31111), tcp(443, 30443)}
	mustEnsure(t, lb, "c", svc, nodes)
	if got := f.ruleFor(t, "80", ip); got.TargetPorts != "31111" {
		t.Errorf("NodePort change not applied: %+v", got)
	}
	if len(f.rules) != 2 {
		t.Errorf("NodePort change must update, not duplicate: %+v", f.rules)
	}

	// Remove a port.
	svc.Spec.Ports = []v1.ServicePort{tcp(443, 30443)}
	mustEnsure(t, lb, "c", svc, nodes)
	if len(f.rules) != 1 || f.rules[0].DestinationPorts != "443" {
		t.Errorf("removed port still has a rule: %+v", f.rules)
	}
	if !f.hasAddr(ip) {
		t.Error("ipalias must be kept while the LB has ports")
	}
}

func TestUpdate_ChangesTargetsViaDiff(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	f.applies = 0

	svc.Spec.Ports = []v1.ServicePort{tcp(80, 30090), tcp(81, 30091)}
	if err := lb.UpdateLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1", "10.0.1.2")); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 2 || f.applies != 1 {
		t.Fatalf("rules=%+v applies=%d", f.rules, f.applies)
	}
	r := f.ruleFor(t, "80", ip)
	if r.TargetPorts != "30090" || r.TargetIP != "10.0.1.1,10.0.1.2" {
		t.Errorf("unexpected rule %+v", r)
	}
}

func TestUpdate_SkipsDeletingService(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	now := metav1.NewTime(time.Now())
	svc.DeletionTimestamp = &now
	if err := lb.UpdateLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 0 {
		t.Error("must not create LB for a deleting service")
	}
}

func TestEnsure_FailedCreateIsReportedAndRetried(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080), tcp(443, 30443), tcp(8080, 30808)})
	nodes := testNodes("10.0.1.1")

	boom := errors.New("boom")
	f.failCreate = func(name string) error {
		if strings.HasSuffix(name, "-tcp-443") {
			return boom
		}
		return nil
	}
	if st, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, nodes); err == nil || !errors.Is(err, boom) || st != nil {
		t.Fatalf("partial failure must return the error and no status, got st=%v err=%v", st, err)
	}
	if len(f.rules) != 2 {
		t.Fatalf("other ports should still be created, got %d rules", len(f.rules))
	}
	if f.applies != 1 {
		t.Errorf("rules that were written must be applied, applies=%d", f.applies)
	}

	// The retry sees the partial state and finishes the job on the same IP.
	f.failCreate = nil
	creates := f.creates
	ip := mustEnsure(t, lb, "c", svc, nodes)
	if ip != "10.0.0.100" || len(f.rules) != 3 || f.creates != creates+1 {
		t.Errorf("retry did not converge: ip=%s rules=%d creates=%d->%d", ip, len(f.rules), creates, f.creates)
	}
	if len(f.addrs) != 1 {
		t.Errorf("retry must reuse the ipalias, got %+v", f.addrs)
	}
}

func TestEnsure_AliasLeftByFailedAttemptIsReused(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	f.failCreate = func(string) error { return errors.New("down") }
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
		t.Fatal("expected error")
	}
	// Another Service takes the next IP in the meantime.
	other := testService("default", "other", []v1.ServicePort{tcp(80, 30081)})
	f.failCreate = nil
	otherIP := mustEnsure(t, lb, "c", other, testNodes("10.0.1.1"))
	if otherIP == "10.0.0.100" {
		t.Fatalf("other service stole the IP reserved by the pending ipalias")
	}
	if ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1")); ip != "10.0.0.100" {
		t.Errorf("retry should resume at 10.0.0.100, got %s", ip)
	}
}

func TestEnsure_ApplyFailureIsRetriedWithoutDiff(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	nodes := testNodes("10.0.1.1")
	f.failApply = errors.New("apply down")
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, nodes); err == nil {
		t.Fatal("apply failure must be returned")
	}
	f.failApply = nil
	mustEnsure(t, lb, "c", svc, nodes)
	if f.applies != 1 {
		t.Errorf("rules were never activated: applies=%d", f.applies)
	}
	mustEnsure(t, lb, "c", svc, nodes)
	if f.applies != 1 {
		t.Errorf("no pending apply expected afterwards: applies=%d", f.applies)
	}
}

func TestEnsure_ForeignRuleNameConflict(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	name := newLBOwner("c", svc).ruleName("tcp", 80)
	f.rules = []vergeos.VNetRule{{Key: 99, Name: name, Description: "someone else", DestinationIP: "10.9.9.9"}}
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
		t.Fatal("expected conflict error")
	}
	if len(f.rules) != 1 || f.rules[0].Description != "someone else" || f.creates+f.updates+f.deletes != 0 {
		t.Errorf("foreign rule must not be touched: %+v", f.rules)
	}
}

func TestEnsure_ConcurrentServicesGetDistinctIPs(t *testing.T) {
	lb, _ := newTestLB("10.0.0.100", "10.0.0.101", "10.0.0.102", "10.0.0.103")
	var wg sync.WaitGroup
	ips := make([]string, 4)
	for i := range ips {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := testService("default", fmt.Sprintf("svc-%d", i), []v1.ServicePort{tcp(80, int32(30000+i))})
			st, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1"))
			if err != nil {
				t.Error(err)
				return
			}
			ips[i] = st.Ingress[0].IP
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, ip := range ips {
		if seen[ip] {
			t.Errorf("duplicate IP allocation: %v", ips)
		}
		seen[ip] = true
	}
}

// --- requested IP ---

func TestEnsure_HonoursLoadBalancerIP(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101", "10.0.0.102")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	svc.Spec.LoadBalancerIP = "10.0.0.102"
	if ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1")); ip != "10.0.0.102" {
		t.Fatalf("ip = %s", ip)
	}
	f.ruleFor(t, "80", "10.0.0.102")
	if !f.hasAddr("10.0.0.102") {
		t.Error("ipalias missing")
	}
}

func TestEnsure_HonoursAnnotation(t *testing.T) {
	lb, _ := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	svc.Annotations = map[string]string{AnnotationLoadBalancerIP: "10.0.0.101"}
	if ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1")); ip != "10.0.0.101" {
		t.Fatalf("ip = %s", ip)
	}
}

func TestEnsure_RequestedIPValidation(t *testing.T) {
	cases := map[string]func(*v1.Service){
		"not in pool": func(s *v1.Service) { s.Spec.LoadBalancerIP = "10.0.0.200" },
		"not an IP":   func(s *v1.Service) { s.Annotations = map[string]string{AnnotationLoadBalancerIP: "nope"} },
		"spec vs annotation": func(s *v1.Service) {
			s.Spec.LoadBalancerIP = "10.0.0.100"
			s.Annotations = map[string]string{AnnotationLoadBalancerIP: "10.0.0.101"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			lb, f := newTestLB("10.0.0.100", "10.0.0.101")
			svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
			mutate(svc)
			if _, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
				t.Fatal("expected error")
			}
			if len(f.rules) != 0 || len(f.addrs) != 0 {
				t.Errorf("nothing may be created: %+v %+v", f.rules, f.addrs)
			}
		})
	}
}

func TestEnsure_RequestedIPInUse(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	first := testService("default", "first", []v1.ServicePort{tcp(80, 30080)})
	first.Spec.LoadBalancerIP = "10.0.0.101"
	mustEnsure(t, lb, "c", first, testNodes("10.0.1.1"))

	second := testService("default", "second", []v1.ServicePort{tcp(80, 30081)})
	second.Spec.LoadBalancerIP = "10.0.0.101"
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", second, testNodes("10.0.1.1")); err == nil {
		t.Fatal("IP used by another service must be rejected")
	}
	if len(f.rules) != 1 {
		t.Errorf("first service's rules changed: %+v", f.rules)
	}

	// An unowned address on the vnet (e.g. a VM lease) blocks the IP too.
	f.addrs = append(f.addrs, vergeos.VNetAddress{Key: 500, IP: "10.0.0.100", Type: "dynamic", Hostname: "some-vm"})
	third := testService("default", "third", []v1.ServicePort{tcp(80, 30082)})
	third.Spec.LoadBalancerIP = "10.0.0.100"
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", third, testNodes("10.0.1.1")); err == nil {
		t.Fatal("IP held by a foreign address must be rejected")
	}
	// ...and automatic allocation skips it.
	auto := testService("default", "auto", []v1.ServicePort{tcp(80, 30083)})
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", auto, testNodes("10.0.1.1")); err == nil {
		t.Fatal("pool is fully used, expected exhaustion")
	}
}

func TestEnsure_IPSurvivesServiceRecreation(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101", "10.0.0.102")
	nodes := testNodes("10.0.1.1")
	// Another service occupies the first IP so "first free" would differ.
	mustEnsure(t, lb, "c", testService("default", "other", []v1.ServicePort{tcp(80, 30001)}), nodes)

	cp := testService("hosted", "kmc-api", []v1.ServicePort{tcp(6443, 30443)})
	cp.Spec.LoadBalancerIP = "10.0.0.102"
	ip := mustEnsure(t, lb, "c", cp, nodes)
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", cp); err != nil {
		t.Fatal(err)
	}
	if f.hasAddr(ip) {
		t.Fatal("ipalias should be released on delete")
	}

	recreated := testService("hosted", "kmc-api", []v1.ServicePort{tcp(6443, 30444)})
	recreated.UID = "new-uid"
	recreated.Spec.LoadBalancerIP = "10.0.0.102"
	if got := mustEnsure(t, lb, "c", recreated, nodes); got != ip {
		t.Errorf("IP changed across recreation: %s -> %s", ip, got)
	}
}

func TestEnsure_KeepsStatusIPWhenRulesVanish(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	mustEnsure(t, lb, "c", testService("default", "other", []v1.ServicePort{tcp(80, 30001)}), testNodes("10.0.1.1"))
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	// Someone deletes our rule (alias remains).
	var keep []vergeos.VNetRule
	for _, r := range f.rules {
		if r.DestinationIP != ip {
			keep = append(keep, r)
		}
	}
	f.rules = keep
	svc.Status.LoadBalancer.Ingress = []v1.LoadBalancerIngress{{IP: ip}}
	if got := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1")); got != ip {
		t.Errorf("IP changed %s -> %s", ip, got)
	}
}

func TestEnsure_ChangingRequestedIPMovesRulesAndReleasesOld(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	old := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	if old != "10.0.0.100" {
		t.Fatal(old)
	}
	svc.Status.LoadBalancer.Ingress = []v1.LoadBalancerIngress{{IP: old}}
	svc.Spec.LoadBalancerIP = "10.0.0.101"
	if got := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1")); got != "10.0.0.101" {
		t.Fatal(got)
	}
	if len(f.rules) != 1 || f.rules[0].DestinationIP != "10.0.0.101" {
		t.Errorf("rules not moved: %+v", f.rules)
	}
	if f.hasAddr("10.0.0.100") || !f.hasAddr("10.0.0.101") {
		t.Errorf("old ipalias must be released: %+v", f.addrs)
	}
}

// --- delete ---

func TestDelete_NoRulesIsNoop(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", testService("default", "x", nil)); err != nil {
		t.Fatal(err)
	}
	if f.applies != 0 {
		t.Error("nothing to apply")
	}
}

func TestDelete_FailureKeepsAliasAndRetries(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080), tcp(443, 30443)})
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))

	f.failDelete = func(name string) error {
		if strings.HasSuffix(name, "-tcp-443") {
			return errors.New("busy")
		}
		return nil
	}
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err == nil {
		t.Fatal("failed delete must be reported")
	}
	if len(f.rules) != 1 || !f.hasAddr(ip) {
		t.Errorf("after partial delete: rules=%+v addrs=%+v", f.rules, f.addrs)
	}
	f.failDelete = nil
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 0 || len(f.addrs) != 0 {
		t.Errorf("retry did not clean up: rules=%+v addrs=%+v", f.rules, f.addrs)
	}
}

func TestDelete_ReleasesOrphanedOwnedAliasOnly(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	owner := newLBOwner("c", svc)
	// Our alias with no rules (failed first attempt) + a foreign alias.
	f.addrs = []vergeos.VNetAddress{
		{Key: 1, IP: "10.0.0.100", Type: "ipalias", Description: owner.String()},
		{Key: 2, IP: "10.0.0.101", Type: "ipalias", Description: "somebody else"},
	}
	svc.Status.LoadBalancer.Ingress = []v1.LoadBalancerIngress{{IP: "10.0.0.100"}, {IP: "10.0.0.101"}}
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err != nil {
		t.Fatal(err)
	}
	if f.hasAddr("10.0.0.100") || !f.hasAddr("10.0.0.101") {
		t.Errorf("addrs = %+v", f.addrs)
	}
}

// --- legacy (prefix scheme) migration ---

func legacyRule(key int, name, destIP, port, target string) vergeos.VNetRule {
	return vergeos.VNetRule{
		Key: vergeos.FlexInt(key), Name: name, Enabled: true, Protocol: "tcp", Direction: "incoming", Action: "translate",
		DestinationIP: destIP, DestinationPorts: port, TargetIP: "10.0.1.1", TargetPorts: target,
	}
}

func TestLegacy_AdoptsOnlyProvablyOwnedRules(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	svc.Status.LoadBalancer.Ingress = []v1.LoadBalancerIngress{{IP: "10.0.0.100"}}
	f.rules = []vergeos.VNetRule{
		legacyRule(1, "k8s-default-web-tcp-80", "10.0.0.100", "80", "30080"),
		legacyRule(2, "k8s-default-web-tcp-9999", "10.0.0.100", "9999", "30999"),   // stale port, same IP
		legacyRule(3, "k8s-default-web-api-tcp-443", "10.0.0.101", "443", "30443"), // web-api: other IP
		legacyRule(4, "k8s-default-web-tcp-81", "10.0.0.101", "81", "30081"),       // right name, IP not ours
	}
	f.addrs = []vergeos.VNetAddress{
		{Key: 1, IP: "10.0.0.100", Type: "ipalias", Hostname: "k8s-lb-default-web"},
		{Key: 2, IP: "10.0.0.101", Type: "ipalias", Hostname: "k8s-lb-default-web-api"},
	}

	if _, ok, _ := lb.GetLoadBalancer(context.Background(), "c", svc); !ok {
		t.Fatal("legacy LB should be recognised")
	}
	if ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1")); ip != "10.0.0.100" {
		t.Fatalf("legacy LB must keep its IP, got %s", ip)
	}
	owner := newLBOwner("c", svc)
	var names []string
	for _, r := range f.rules {
		names = append(names, r.Name)
	}
	if len(f.rules) != 4 {
		t.Fatalf("expected adopted rule + 3 untouched rules (stale port, web-api, wrong IP), got %v", names)
	}
	r := f.ruleFor(t, "80", "10.0.0.100")
	if r.Name != owner.ruleName("tcp", 80) || r.Description != owner.String() {
		t.Errorf("legacy rule not migrated in place: %+v", r)
	}
	if f.creates != 0 {
		t.Errorf("adoption must not recreate rules: creates=%d", f.creates)
	}
	f.ruleFor(t, "443", "10.0.0.101")
	f.ruleFor(t, "81", "10.0.0.101")
	if stale := f.ruleFor(t, "9999", "10.0.0.100"); stale.Description != "" {
		t.Errorf("legacy rule for a port not in the spec cannot be proven and must stay untouched: %+v", stale)
	}
	if a := f.addrs[0]; a.Description != owner.String() {
		t.Errorf("legacy ipalias should be stamped on adoption: %+v", a)
	}
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 3 || !f.hasAddr("10.0.0.101") || f.hasAddr("10.0.0.100") {
		t.Errorf("delete removed the wrong things: rules=%+v addrs=%+v", f.rules, f.addrs)
	}
}

func TestLegacy_NotAdoptedWithoutStatusIP(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	f.rules = []vergeos.VNetRule{legacyRule(1, "k8s-default-web-tcp-80", "10.0.0.100", "80", "30080")}
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)}) // no status IP

	if _, ok, _ := lb.GetLoadBalancer(context.Background(), "c", svc); ok {
		t.Error("cannot prove ownership, must not report exists")
	}
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 1 {
		t.Error("unprovable legacy rule must never be deleted")
	}
	// Ensure goes elsewhere rather than stealing the legacy IP.
	if ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1")); ip != "10.0.0.101" {
		t.Errorf("ip = %s, legacy-used IP must be skipped", ip)
	}
}

func TestLegacy_DashAmbiguousNamesNotAdopted(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	// The old prefix bug made a-b/c report a/b-c's IP, so both statuses hold it.
	s1 := testService("a-b", "c", []v1.ServicePort{tcp(80, 30080)})
	s2 := testService("a", "b-c", []v1.ServicePort{tcp(80, 30081)})
	for _, s := range []*v1.Service{s1, s2} {
		s.Status.LoadBalancer.Ingress = []v1.LoadBalancerIngress{{IP: "10.0.0.100"}}
	}
	// Legacy objects belong to a/b-c (NodePort 30081); same names as a-b/c would use.
	f.rules = []vergeos.VNetRule{legacyRule(1, "k8s-a-b-c-tcp-80", "10.0.0.100", "80", "30081")}
	f.addrs = []vergeos.VNetAddress{{Key: 1, IP: "10.0.0.100", Type: "ipalias", Hostname: "k8s-lb-a-b-c"}}

	if _, ok, _ := lb.GetLoadBalancer(context.Background(), "c", s1); ok {
		t.Error("a-b/c must not claim a/b-c's legacy rule")
	}
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", s1); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 1 || !f.hasAddr("10.0.0.100") {
		t.Fatalf("a-b/c delete destroyed a/b-c's objects: rules=%+v addrs=%+v", f.rules, f.addrs)
	}
	// a-b/c gets its own IP and leaves a/b-c's rule alone.
	if ip := mustEnsure(t, lb, "c", s1, testNodes("10.0.1.1")); ip != "10.0.0.101" {
		t.Errorf("a-b/c ip = %s", ip)
	}
	if got := f.ruleFor(t, "80", "10.0.0.100"); got.TargetPorts != "30081" || got.Description != "" {
		t.Errorf("a/b-c legacy rule modified: %+v", got)
	}
	// The real owner (matching NodePort) still adopts it.
	if ip := mustEnsure(t, lb, "c", s2, testNodes("10.0.1.1")); ip != "10.0.0.100" {
		t.Errorf("a/b-c ip = %s", ip)
	}
	if f.creates != 1 { // only a-b/c's new rule
		t.Errorf("a/b-c should be adopted in place, creates=%d", f.creates)
	}
}

// --- orphans, ownership checks, apply recovery ---

func TestDelete_RemovesAliasOrphanedByFailedEnsure(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	f.failCreate = func(string) error { return errors.New("down") }
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
		t.Fatal("expected error")
	}
	if len(f.addrs) != 1 || len(f.rules) != 0 {
		t.Fatalf("setup: expected an orphan alias, got addrs=%+v rules=%+v", f.addrs, f.rules)
	}
	// Service deleted before any successful Ensure: empty status, no rules.
	f.failCreate = nil
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err != nil {
		t.Fatal(err)
	}
	if len(f.addrs) != 0 {
		t.Errorf("orphaned alias leaked: %+v", f.addrs)
	}
}

func TestDelete_OrphanProbeLeavesOtherOwnersAliases(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	other := testService("default", "other", []v1.ServicePort{tcp(80, 30081)})
	f.addrs = []vergeos.VNetAddress{{Key: 1, IP: "10.0.0.100", Type: "ipalias", Description: newLBOwner("c", other).String()}}
	svc := testService("default", "web", nil)
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err != nil {
		t.Fatal(err)
	}
	if !f.hasAddr("10.0.0.100") {
		t.Error("alias of another owner removed")
	}
}

func TestEnsure_DoesNotClaimForeignAliasAtOwnedRuleIP(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	owner := newLBOwner("c", svc)
	f.rules = []vergeos.VNetRule{{Key: 1, Name: owner.ruleName("tcp", 80), Description: owner.String(), Enabled: true,
		Protocol: "tcp", Direction: "incoming", Action: "translate", DestinationIP: "10.0.0.100", DestinationPorts: "80",
		TargetIP: "10.0.1.1", TargetPorts: "30080"}}
	f.addrs = []vergeos.VNetAddress{{Key: 1, IP: "10.0.0.100", Type: "dynamic", Hostname: "vm", Description: "vm lease"}}
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
		t.Fatal("must not claim a foreign address")
	}
	if f.addrs[0].Description != "vm lease" {
		t.Errorf("foreign address modified: %+v", f.addrs[0])
	}
}

func TestOwnedRuleOrderIsDeterministic(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	owner := newLBOwner("c", svc)
	mk := func(key int, ip string, port string) vergeos.VNetRule {
		return vergeos.VNetRule{Key: vergeos.FlexInt(key), Name: fmt.Sprint("r", key), Description: owner.String(), DestinationIP: ip, DestinationPorts: port}
	}
	f.rules = []vergeos.VNetRule{mk(9, "10.0.0.101", "81"), mk(2, "10.0.0.100", "80")}
	st, ok, err := lb.GetLoadBalancer(context.Background(), "c", svc)
	if err != nil || !ok || st.Ingress[0].IP != "10.0.0.100" {
		t.Errorf("want lowest-key rule's IP 10.0.0.100, got %v %v %v", st, ok, err)
	}
}

func TestEnsure_AppliesWhenVNetNeedsFWApply(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	applies := f.applies

	mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	if f.applies != applies {
		t.Fatal("no apply expected when vnet is clean")
	}
	// Simulate a restart after a crash between rule write and apply:
	// fresh LB (no in-memory flag), vnet still flagged.
	f.needFW = true
	lb2 := newLoadBalancer(fakeRules{f}, fakeNet{f}, fakeAddrs{f}, lb.config)
	mustEnsure(t, lb2, "c", svc, testNodes("10.0.1.1"))
	if f.applies != applies+1 {
		t.Errorf("need_fw_apply must trigger an apply, applies=%d", f.applies)
	}
}

// --- loadBalancerSourceRanges (#12) ---

func TestSourceRanges_EnforcedAsRuleSourceIP(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080), tcp(443, 30443)})
	svc.Spec.LoadBalancerSourceRanges = []string{"192.168.0.0/16", " 10.1.2.3/8 ", "192.168.0.0/16"}
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))

	// Canonical network form, sorted, deduplicated, on every rule.
	for _, port := range []string{"80", "443"} {
		if got := f.ruleFor(t, port, ip).SourceIP; got != "10.0.0.0/8,192.168.0.0/16" {
			t.Errorf("port %s source_ip = %q", port, got)
		}
	}

	// Unchanged ranges: no writes.
	creates, updates, deletes, applies := f.creates, f.updates, f.deletes, f.applies
	mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	if f.creates != creates || f.updates != updates || f.deletes != deletes || f.applies != applies {
		t.Error("idempotent ensure wrote")
	}

	// Changed ranges are reconciled in place and applied.
	svc.Spec.LoadBalancerSourceRanges = []string{"172.16.0.0/12"}
	mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	if f.updates != updates+2 || f.applies != applies+1 {
		t.Errorf("updates %d->%d applies %d->%d", updates, f.updates, applies, f.applies)
	}
	if got := f.ruleFor(t, "80", ip).SourceIP; got != "172.16.0.0/12" {
		t.Errorf("source_ip = %q after change", got)
	}

	// Removing the restriction clears source_ip.
	svc.Spec.LoadBalancerSourceRanges = nil
	mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	for _, port := range []string{"80", "443"} {
		if got := f.ruleFor(t, port, ip).SourceIP; got != "" {
			t.Errorf("port %s source_ip = %q after removal, want empty", port, got)
		}
	}
}

func TestSourceRanges_UpdateLoadBalancerRestrictsExistingOpenRule(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	svc.Spec.LoadBalancerSourceRanges = []string{"10.0.0.0/8"}
	if err := lb.UpdateLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err != nil {
		t.Fatal(err)
	}
	if got := f.ruleFor(t, "80", ip).SourceIP; got != "10.0.0.0/8" {
		t.Errorf("source_ip = %q", got)
	}
}

func TestSourceRanges_Annotation(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	svc.Annotations = map[string]string{annotationSourceRanges: "10.0.0.0/8, 192.168.1.0/24"}
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	if got := f.ruleFor(t, "80", ip).SourceIP; got != "10.0.0.0/8,192.168.1.0/24" {
		t.Errorf("source_ip = %q", got)
	}
}

func TestSourceRanges_AllowAllIsUnrestricted(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	svc.Spec.LoadBalancerSourceRanges = []string{"10.0.0.0/8", "0.0.0.0/0"}
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	if got := f.ruleFor(t, "80", ip).SourceIP; got != "" {
		t.Errorf("source_ip = %q, want empty for 0.0.0.0/0", got)
	}
}

func TestSourceRanges_InvalidFailsClosed(t *testing.T) {
	for _, bad := range []string{"not-a-cidr", "10.0.0.1", "10.0.0.0/33", "10.0.0.0/8'"} {
		lb, f := newTestLB("10.0.0.100")
		svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
		svc.Spec.LoadBalancerSourceRanges = []string{"10.0.0.0/8", bad}
		if _, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil ||
			!strings.Contains(err.Error(), "loadBalancerSourceRanges") {
			t.Errorf("%q: expected source range error, got %v", bad, err)
		}
		if len(f.rules) != 0 || len(f.addrs) != 0 {
			t.Errorf("%q: nothing may be created (rules=%d addrs=%d)", bad, len(f.rules), len(f.addrs))
		}
	}
}

func TestSourceRanges_InvalidUpdateKeepsExistingRules(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	svc.Spec.LoadBalancerSourceRanges = []string{"10.0.0.0/8"}
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	svc.Spec.LoadBalancerSourceRanges = []string{"garbage"}
	if err := lb.UpdateLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
		t.Fatal("expected error")
	}
	if got := f.ruleFor(t, "80", ip).SourceIP; got != "10.0.0.0/8" {
		t.Errorf("existing restriction lost: %q", got)
	}
}

// --- externalTrafficPolicy=Local (#13) ---

func TestEnsure_ExternalTrafficPolicyLocalRejected(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	svc.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyLocal
	svc.Spec.HealthCheckNodePort = 31000
	_, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1", "10.0.1.2"))
	if err == nil || !strings.Contains(err.Error(), "externalTrafficPolicy=Local") {
		t.Fatalf("expected externalTrafficPolicy error, got %v", err)
	}
	if err := lb.UpdateLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
		t.Error("UpdateLoadBalancer must reject Local too")
	}
	if len(f.rules) != 0 || len(f.addrs) != 0 {
		t.Errorf("rejected Service must not create anything: rules=%d addrs=%d", len(f.rules), len(f.addrs))
	}

	// Cluster (and unset) still work; Local Services can still be deleted.
	svc.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyCluster
	mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	svc.Spec.ExternalTrafficPolicy = v1.ServiceExternalTrafficPolicyLocal
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 0 || len(f.addrs) != 0 {
		t.Errorf("delete left rules=%d addrs=%d", len(f.rules), len(f.addrs))
	}
}

// --- pool probing (#17) ---

func bigPool(n int) []string {
	var pool []string
	for i := 0; i < n; i++ {
		pool = append(pool, fmt.Sprintf("10.9.%d.%d", i/250, i%250+1))
	}
	return pool
}

func TestDelete_DoesNotProbePool(t *testing.T) {
	pool := bigPool(500)
	lb, f := newTestLB(pool...)
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	other := testService("default", "other", []v1.ServicePort{tcp(80, 30081)})
	mustEnsure(t, lb, "c", other, testNodes("10.0.1.1"))
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	// Orphan alias of ours at an IP no rule or status mentions.
	owner := newLBOwner("c", svc)
	f.addrs = append(f.addrs, vergeos.VNetAddress{Key: 900, IP: pool[300], Type: "ipalias", Description: owner.String()})

	f.addrGets, f.addrLists = 0, 0
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err != nil {
		t.Fatal(err)
	}
	if f.addrGets != 0 || f.addrLists != 1 {
		t.Errorf("delete made %d GetByIP and %d list calls, want 0 and 1", f.addrGets, f.addrLists)
	}
	if f.hasAddr(ip) || f.hasAddr(pool[300]) {
		t.Errorf("own aliases not released: %+v", f.addrs)
	}
	if !f.hasAddr(pool[0]) || len(f.addrs) != 1 {
		t.Errorf("other service's alias must remain: %+v", f.addrs)
	}
}

func TestEnsure_AllocationDoesNotProbePerIP(t *testing.T) {
	lb, f := newTestLB(bigPool(500)...)
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	// One GetByIP for ensureAddress at the chosen IP; the pool scan uses a single list.
	if f.addrGets > 1 || f.addrLists != 1 {
		t.Errorf("allocation made %d GetByIP and %d list calls", f.addrGets, f.addrLists)
	}
}

func TestSourceRanges_BlankListFailsClosed(t *testing.T) {
	for name, svc := range map[string]*v1.Service{
		"annotation comma":      {ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annotationSourceRanges: ","}}},
		"annotation whitespace": {ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annotationSourceRanges: "  "}}},
		"spec blanks":           {Spec: v1.ServiceSpec{LoadBalancerSourceRanges: []string{"", " "}}},
	} {
		if got, err := sourceRanges(svc); err == nil {
			t.Errorf("%s: expected error, got %q", name, got)
		}
	}
	// Truly unset is unrestricted.
	if got, err := sourceRanges(&v1.Service{}); err != nil || got != "" {
		t.Errorf("unset: got %q, %v", got, err)
	}
	// End to end: nothing is created for a blank annotation.
	lb, f := newTestLB("10.0.0.100")
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	svc.Annotations = map[string]string{annotationSourceRanges: ","}
	if _, err := lb.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
		t.Error("expected error")
	}
	if len(f.rules) != 0 || len(f.addrs) != 0 {
		t.Errorf("created rules=%d addrs=%d", len(f.rules), len(f.addrs))
	}
}

func TestSourceRanges_DualStack(t *testing.T) {
	svc := &v1.Service{Spec: v1.ServiceSpec{LoadBalancerSourceRanges: []string{"fd00::/8", "10.0.0.0/8", "2001:db8::/32"}}}
	if got, err := sourceRanges(svc); err != nil || got != "10.0.0.0/8" {
		t.Errorf("dual-stack: got %q, %v; want IPv4 only", got, err)
	}
	svc.Spec.LoadBalancerSourceRanges = []string{"fd00::/8", "2001:db8::/32"}
	if got, err := sourceRanges(svc); err == nil {
		t.Errorf("IPv6-only must error, got %q", got)
	}
	// Garbage is still an error even next to valid IPv4.
	svc.Spec.LoadBalancerSourceRanges = []string{"10.0.0.0/8", "fd00::/999"}
	if _, err := sourceRanges(svc); err == nil {
		t.Error("malformed IPv6 entry must error")
	}
}

func TestSourceRanges_SpecWinsOverAnnotation(t *testing.T) {
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annotationSourceRanges: "192.168.0.0/16"}},
		Spec:       v1.ServiceSpec{LoadBalancerSourceRanges: []string{"10.0.0.0/8"}},
	}
	if got, err := sourceRanges(svc); err != nil || got != "10.0.0.0/8" {
		t.Errorf("spec set: got %q, %v", got, err)
	}
	svc.Spec.LoadBalancerSourceRanges = nil
	if got, err := sourceRanges(svc); err != nil || got != "192.168.0.0/16" {
		t.Errorf("annotation only: got %q, %v", got, err)
	}
	// A valid spec is not poisoned by a blank annotation.
	svc.Spec.LoadBalancerSourceRanges = []string{"10.0.0.0/8"}
	svc.Annotations[annotationSourceRanges] = ","
	if got, err := sourceRanges(svc); err != nil || got != "10.0.0.0/8" {
		t.Errorf("spec with blank annotation: got %q, %v", got, err)
	}
}

// --- same-vnet / routed client reachability (#41) ---

func rulesByRole(f *fakeVNet) map[string]vergeos.VNetRule {
	m := map[string]vergeos.VNetRule{}
	for _, r := range f.rules {
		m[ruleKey(r.Direction, r.Action, r.Protocol, r.DestinationPorts)] = r
	}
	return m
}

func TestEnsure_DefaultHasNoSNATOrAcceptRules(t *testing.T) {
	lb, f := newTestLB("10.0.0.100")
	mustEnsure(t, lb, "c", testService("default", "web", []v1.ServicePort{tcp(80, 30080)}), testNodes("10.0.1.1"))
	if len(f.rules) != 1 {
		t.Fatalf("want only the DNAT rule by default, got %+v", f.rules)
	}
}

func TestEnsure_SourceNATAndAcceptRules(t *testing.T) {
	lb, f := newTestLB("10.0.0.100", "10.0.0.101")
	lb.config.SourceNATTarget = "10.0.0.1"
	lb.config.AcceptRules = true
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080), {Port: 53, NodePort: 30053, Protocol: v1.ProtocolUDP}})
	svc.Spec.LoadBalancerSourceRanges = []string{"192.168.0.0/16"}
	ip := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.2", "10.0.1.1"))
	if ip != "10.0.0.100" {
		t.Fatalf("ip = %s", ip)
	}
	if len(f.rules) != 6 || f.applies != 1 {
		t.Fatalf("rules=%d applies=%d, want 6 rules and one apply: %+v", len(f.rules), f.applies, f.rules)
	}
	by := rulesByRole(f)
	owner := newLBOwner("c", svc)

	dnat := by["incoming/translate/tcp/80"]
	if dnat.DestinationIP != ip || dnat.TargetIP != "10.0.1.1,10.0.1.2" || dnat.TargetPorts != "30080" {
		t.Errorf("dnat %+v", dnat)
	}
	snat := by["outgoing/translate/tcp/30080"]
	if snat.Name != owner.ruleName("tcp", 80)+"-snat" || snat.Description != owner.String() || !snat.Enabled ||
		snat.DestinationIP != "10.0.1.1,10.0.1.2" || snat.TargetIP != "10.0.0.1" || snat.SourceIP != "" {
		t.Errorf("snat %+v", snat)
	}
	if u := by["outgoing/translate/udp/30053"]; u.Protocol != "udp" || u.TargetIP != "10.0.0.1" {
		t.Errorf("udp snat %+v", u)
	}
	acc := by["incoming/accept/tcp/80"]
	if acc.Name != owner.ruleName("tcp", 80)+"-accept" || acc.DestinationIP != ip || acc.SourceIP != "192.168.0.0/16" ||
		acc.TargetIP != "" || acc.TargetPorts != "" {
		t.Errorf("accept %+v", acc)
	}

	// Idempotent: SNAT rules must not be mistaken for the LB IP, and nothing is rewritten.
	creates, updates, deletes, applies, lists := f.creates, f.updates, f.deletes, f.applies, f.addrLists
	if ip2 := mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1", "10.0.1.2")); ip2 != ip {
		t.Fatalf("ip changed to %s", ip2)
	}
	if f.creates != creates || f.updates != updates || f.deletes != deletes || f.applies != applies {
		t.Error("idempotent ensure wrote")
	}
	if f.addrLists != lists {
		t.Error("SNAT rule destinations (node IPs) were treated as moved LB IPs")
	}
	if st, found, err := lb.GetLoadBalancer(context.Background(), "c", svc); err != nil || !found || st.Ingress[0].IP != ip {
		t.Errorf("GetLoadBalancer = %v %v %v", st, found, err)
	}

	// Node changes are reflected in the SNAT rule destination.
	mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1", "10.0.1.3"))
	if got := rulesByRole(f)["outgoing/translate/tcp/30080"].DestinationIP; got != "10.0.1.1,10.0.1.3" {
		t.Errorf("snat destination after node change = %q", got)
	}

	// Turning the options off removes the extra rules (they are owned) and keeps the DNAT rule.
	lb.config.SourceNATTarget = ""
	lb.config.AcceptRules = false
	if mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1", "10.0.1.3")) != ip {
		t.Fatal("ip changed")
	}
	if len(f.rules) != 2 {
		t.Fatalf("want 2 DNAT rules left, got %+v", f.rules)
	}

	// Delete removes every owned rule kind and the alias.
	lb.config.SourceNATTarget = "10.0.0.1"
	lb.config.AcceptRules = true
	mustEnsure(t, lb, "c", svc, testNodes("10.0.1.1"))
	if err := lb.EnsureLoadBalancerDeleted(context.Background(), "c", svc); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 0 || f.hasAddr(ip) {
		t.Errorf("leftovers: rules=%+v addrs=%+v", f.rules, f.addrs)
	}
}
