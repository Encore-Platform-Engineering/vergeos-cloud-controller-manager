package vergeos

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	v1 "k8s.io/api/core/v1"
)

// lbAPIFake is a stateful VergeOS API for the LoadBalancer path: VMs with NICs
// (read-only), vnet rules and addresses (full CRUD), vnet get and apply. It
// records every request as "METHOD path?filter" so tests can assert the exact
// filter strings the real govergeos client sends.
type lbAPIFake struct {
	mu       sync.Mutex
	vms      []map[string]any
	nics     []map[string]any
	rules    []map[string]any
	addrs    []map[string]any
	nextKey  int
	requests []string
	applied  []string // raw bodies of POST /vnet_actions
}

var (
	vnetFilterRe   = regexp.MustCompile(`vnet eq (\d+)`)
	ipFilterRe     = regexp.MustCompile(`ip eq '([^']+)'`)
	nameFilterRe   = regexp.MustCompile(`name eq '([^']+)'`)
	machineFilter  = regexp.MustCompile(`machine eq (\d+)`)
	collectionPath = regexp.MustCompile(`^/api/v4/(vnet_rules|vnet_addresses)(?:/(\d+))?$`)
)

func (f *lbAPIFake) reqs(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func (f *lbAPIFake) mutations() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if !strings.HasPrefix(r, "GET ") {
			n++
		}
	}
	return n
}

func (f *lbAPIFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	filter := r.URL.Query().Get("filter")
	line := r.Method + " " + r.URL.Path
	if filter != "" {
		line += "?" + filter
	}
	f.requests = append(f.requests, line)

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	switch {
	case r.URL.Path == "/version.json":
		_, _ = w.Write([]byte(`{"version":"26.0.0"}`))
	case r.URL.Path == "/api/v4/vms":
		var out []map[string]any
		for _, vm := range f.vms {
			if m := nameFilterRe.FindStringSubmatch(filter); m != nil && vm["name"] == m[1] {
				out = append(out, vm)
			}
		}
		_ = enc.Encode(out)
	case r.URL.Path == "/api/v4/machine_nics":
		var out []map[string]any
		if m := machineFilter.FindStringSubmatch(filter); m != nil {
			for _, n := range f.nics {
				if fmt.Sprint(n["machine"]) == m[1] {
					out = append(out, n)
				}
			}
		}
		_ = enc.Encode(out)
	case strings.HasPrefix(r.URL.Path, "/api/v4/vnets/"):
		_ = enc.Encode(map[string]any{"$key": 5, "need_fw_apply": len(f.applied) == 0})
	case r.URL.Path == "/api/v4/vnet_actions":
		body, _ := io.ReadAll(r.Body)
		f.applied = append(f.applied, string(body))
		_ = enc.Encode(map[string]any{})
	case collectionPath.MatchString(r.URL.Path):
		m := collectionPath.FindStringSubmatch(r.URL.Path)
		f.serveCollection(w, r, m[1], m[2], filter)
	default:
		http.NotFound(w, r)
	}
}

func (f *lbAPIFake) serveCollection(w http.ResponseWriter, r *http.Request, coll, id, filter string) {
	store := &f.rules
	if coll == "vnet_addresses" {
		store = &f.addrs
	}
	enc := json.NewEncoder(w)
	find := func() int {
		for i, o := range *store {
			if fmt.Sprint(o["$key"]) == id {
				return i
			}
		}
		return -1
	}
	switch {
	case r.Method == http.MethodGet && id == "":
		var out []map[string]any
		for _, o := range *store {
			if m := vnetFilterRe.FindStringSubmatch(filter); m == nil || fmt.Sprint(o["vnet"]) != m[1] {
				continue
			}
			if m := ipFilterRe.FindStringSubmatch(filter); m != nil && o["ip"] != m[1] {
				continue
			}
			out = append(out, o)
		}
		_ = enc.Encode(out)
	case r.Method == http.MethodGet:
		if i := find(); i >= 0 {
			_ = enc.Encode((*store)[i])
			return
		}
		http.NotFound(w, r)
	case r.Method == http.MethodPost:
		var o map[string]any
		_ = json.NewDecoder(r.Body).Decode(&o)
		f.nextKey++
		o["$key"] = f.nextKey
		*store = append(*store, o)
		_ = enc.Encode(map[string]any{"$key": f.nextKey})
	case r.Method == http.MethodPut:
		i := find()
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		var patch map[string]any
		_ = json.NewDecoder(r.Body).Decode(&patch)
		for k, v := range patch {
			(*store)[i][k] = v
		}
		_ = enc.Encode(map[string]any{})
	case r.Method == http.MethodDelete:
		i := find()
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		*store = append((*store)[:i], (*store)[i+1:]...)
		_ = enc.Encode(map[string]any{})
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

// ruleView is the interesting part of a stored rule.
type ruleView struct {
	Name, Proto, Dir, Action, DestIP, DestPorts, TargetIP, TargetPorts, Source, Desc string
}

func (f *lbAPIFake) ruleViews() []ruleView {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ruleView
	for _, r := range f.rules {
		s := func(k string) string { v, _ := r[k].(string); return v }
		out = append(out, ruleView{s("name"), s("protocol"), s("direction"), s("action"), s("destination_ip"), s("destination_ports"), s("target_ip"), s("target_ports"), s("source_ip"), s("description")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// lbRoundTripCloud wires newCloud to a fake API holding two dual-NIC nodes:
// node vnet 10 (InternalIP) and LB vnet 5.
func lbRoundTripCloud(t *testing.T) (*cloud, *lbAPIFake, []*v1.Node) {
	t.Helper()
	fake := &lbAPIFake{
		vms: []map[string]any{
			{"$key": 1, "uuid": testUUID, "name": "node-a", "machine": 101},
			{"$key": 2, "uuid": "11111111-2222-3333-4444-555555555555", "name": "node-b", "machine": 102},
		},
		nics: []map[string]any{
			{"$key": 1, "machine": 101, "vnet": 10, "macaddress": "52:54:00:00:00:01", "ipaddress": "198.51.100.1"},
			{"$key": 2, "machine": 101, "vnet": 5, "macaddress": "52:54:00:00:00:02", "ipaddress": "10.5.0.11"},
			{"$key": 3, "machine": 102, "vnet": 10, "macaddress": "52:54:00:00:01:01", "ipaddress": "198.51.100.2"},
			{"$key": 4, "machine": 102, "vnet": 5, "macaddress": "52:54:00:00:01:02", "ipaddress": "10.5.0.12"},
		},
	}
	srv := httptest.NewTLSServer(fake)
	t.Cleanup(srv.Close)
	setEnv(t, map[string]string{envHost: srv.URL, envAPIKey: "k", envCAFile: writeServerCA(t, srv)})
	c, err := newCloud(strings.NewReader(`{"nodeNetworkID":10,"loadBalancer":{"enabled":true,"networkID":5,"ipPool":["10.5.0.100","10.5.0.101"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	nodes := []*v1.Node{nodeNamed("node-b"), nodeNamed("node-a")}
	for i, ip := range []string{"198.51.100.2", "198.51.100.1"} {
		nodes[i].Status.Addresses = []v1.NodeAddress{{Type: v1.NodeInternalIP, Address: ip}}
	}
	return c, fake, nodes
}

// EnsureLoadBalancer / UpdateLoadBalancer / EnsureLoadBalancerDeleted through
// newCloud -> real govergeos client -> HTTPS, asserting request filters and
// the resulting rules.
func TestLoadBalancer_RoundTripOverHTTP(t *testing.T) {
	ctx := context.Background()
	c, fake, nodes := lbRoundTripCloud(t)
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080), {Port: 53, NodePort: 30053, Protocol: v1.ProtocolUDP}})
	svc.Spec.LoadBalancerSourceRanges = []string{"192.168.0.0/16", "10.0.0.0/8"}

	status, err := c.lb.EnsureLoadBalancer(ctx, "c1", svc, nodes)
	if err != nil {
		t.Fatalf("EnsureLoadBalancer: %v", err)
	}
	if len(status.Ingress) != 1 || status.Ingress[0].IP != "10.5.0.100" {
		t.Fatalf("status = %+v, want first pool IP 10.5.0.100", status)
	}

	owner := newLBOwner("c1", svc).String()
	targets := "10.5.0.11,10.5.0.12" // the nodes' vnet-5 NICs, not their vnet-10 InternalIPs
	want := []ruleView{
		{newLBOwner("c1", svc).ruleName("tcp", 80), "tcp", "incoming", "translate", "10.5.0.100", "80", targets, "30080", "10.0.0.0/8,192.168.0.0/16", owner},
		{newLBOwner("c1", svc).ruleName("udp", 53), "udp", "incoming", "translate", "10.5.0.100", "53", targets, "30053", "10.0.0.0/8,192.168.0.0/16", owner},
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Name < want[j].Name })
	got := fake.ruleViews()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rules =\n%+v\nwant\n%+v", got, want)
	}

	fake.mu.Lock()
	if len(fake.addrs) != 1 || fake.addrs[0]["ip"] != "10.5.0.100" || fake.addrs[0]["type"] != "ipalias" || fake.addrs[0]["vnet"] != float64(5) || fake.addrs[0]["description"] != owner {
		t.Errorf("addresses = %v, want one ipalias 10.5.0.100 on vnet 5 owned by the Service", fake.addrs)
	}
	if len(fake.applied) != 1 || !strings.Contains(fake.applied[0], `"vnet":5`) {
		t.Errorf("rules must be applied once on vnet 5, got %v", fake.applied)
	}
	fake.mu.Unlock()

	for prefix, wantReq := range map[string]string{
		"GET /api/v4/vnet_rules":     "GET /api/v4/vnet_rules?vnet eq 5",
		"GET /api/v4/vnet_addresses": "GET /api/v4/vnet_addresses?vnet eq 5 and ip eq '10.5.0.100'",
		"GET /api/v4/vms":            "GET /api/v4/vms?name eq 'node-a' and is_snapshot eq false",
		"GET /api/v4/machine_nics":   "GET /api/v4/machine_nics?machine eq 101",
	} {
		if reqs := fake.reqs(prefix); !contains(reqs, wantReq) {
			t.Errorf("expected request %q, got %q", wantReq, reqs)
		}
	}
	for _, r := range fake.reqs("GET /api/v4/vnet_") {
		if m := vnetFilterRe.FindStringSubmatch(r); m != nil && m[1] != "5" {
			t.Errorf("request %q must only touch the LB vnet 5", r)
		}
	}

	// A second sync with nothing changed must write nothing.
	before := fake.mutations()
	if err := c.lb.UpdateLoadBalancer(ctx, "c1", svc, nodes); err != nil {
		t.Fatalf("UpdateLoadBalancer: %v", err)
	}
	if after := fake.mutations(); after != before {
		t.Errorf("idempotent update made %d writes: %q", after-before, fake.reqs("POST")[len(fake.reqs("POST"))-1:])
	}

	// Delete removes the rules and the ipalias and applies.
	if err := c.lb.EnsureLoadBalancerDeleted(ctx, "c1", svc); err != nil {
		t.Fatalf("EnsureLoadBalancerDeleted: %v", err)
	}
	fake.mu.Lock()
	if len(fake.rules) != 0 || len(fake.addrs) != 0 {
		t.Errorf("delete left rules=%v addrs=%v", fake.rules, fake.addrs)
	}
	if len(fake.applied) != 2 {
		t.Errorf("delete must apply the rule removal, applies=%d", len(fake.applied))
	}
	fake.mu.Unlock()
	if st, exists, _ := c.lb.GetLoadBalancer(ctx, "c1", svc); exists || st != nil {
		t.Errorf("GetLoadBalancer after delete = %v, %v", st, exists)
	}
}

// With no node on the LB vnet, Ensure fails over HTTP without creating anything.
func TestLoadBalancer_RoundTripNoNodeOnLBVnet(t *testing.T) {
	c, fake, nodes := lbRoundTripCloud(t)
	fake.mu.Lock()
	fake.nics = []map[string]any{ // vnet-10 NICs only
		{"$key": 1, "machine": 101, "vnet": 10, "macaddress": "52:54:00:00:00:01", "ipaddress": "198.51.100.1"},
		{"$key": 3, "machine": 102, "vnet": 10, "macaddress": "52:54:00:00:01:01", "ipaddress": "198.51.100.2"},
	}
	fake.mu.Unlock()

	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	_, err := c.lb.EnsureLoadBalancer(context.Background(), "c1", svc, nodes)
	if err == nil || !strings.Contains(err.Error(), "vnet "+strconv.Itoa(5)) {
		t.Fatalf("expected an error naming LB vnet 5, got %v", err)
	}
	if n := fake.mutations(); n != 0 {
		t.Errorf("no writes expected, got %q", fake.requests)
	}
}
