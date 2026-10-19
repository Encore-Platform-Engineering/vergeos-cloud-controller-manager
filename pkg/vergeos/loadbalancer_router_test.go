package vergeos

import (
	"context"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
)

// newTestRouter serves default vnet 5 (pool 10.5.0.x) and vnet 6 (pool
// 10.6.0.x), each backed by its own fake vnet.
func newTestRouter() (*lbRouter, *fakeVNet, *fakeVNet) {
	f5, f6 := &fakeVNet{}, &fakeVNet{}
	w5 := newLoadBalancer(fakeRules{f5}, fakeNet{f5}, fakeAddrs{f5}, internalIPTargets{}, lbConfig{Enabled: true, NetworkID: 5, IPPool: []string{"10.5.0.100", "10.5.0.101"}})
	w6 := newLoadBalancer(fakeRules{f6}, fakeNet{f6}, fakeAddrs{f6}, internalIPTargets{}, lbConfig{Enabled: true, NetworkID: 6, IPPool: []string{"10.6.0.100"}})
	return newLBRouter(w5, w6), f5, f6
}

func annotated(svc *v1.Service, networkID string) *v1.Service {
	svc.Annotations = map[string]string{AnnotationLoadBalancerNetworkID: networkID}
	return svc
}

func ensureVia(t *testing.T, r *lbRouter, svc *v1.Service) string {
	t.Helper()
	st, err := r.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1"))
	if err != nil {
		t.Fatalf("EnsureLoadBalancer: %v", err)
	}
	return st.Ingress[0].IP
}

func TestRouter_DefaultAndAnnotatedVnet(t *testing.T) {
	r, f5, f6 := newTestRouter()
	ports := []v1.ServicePort{tcp(80, 30080)}

	if ip := ensureVia(t, r, testService("default", "plain", ports)); ip != "10.5.0.100" {
		t.Errorf("unannotated Service got %s, want the default vnet's pool IP", ip)
	}
	if ip := ensureVia(t, r, annotated(testService("default", "other", ports), "6")); ip != "10.6.0.100" {
		t.Errorf("annotated Service got %s, want vnet 6's pool IP", ip)
	}
	if len(f5.rules) != 1 || len(f6.rules) != 1 || !f5.hasAddr("10.5.0.100") || !f6.hasAddr("10.6.0.100") || f5.hasAddr("10.6.0.100") {
		t.Errorf("each Service must live only on its own vnet: vnet5 rules=%d vnet6 rules=%d", len(f5.rules), len(f6.rules))
	}
}

// Each vnet allocates from its own pool: exhausting vnet 6's single IP must not touch vnet 5.
func TestRouter_PoolsAreIndependent(t *testing.T) {
	r, _, _ := newTestRouter()
	ports := []v1.ServicePort{tcp(80, 30080)}
	ensureVia(t, r, annotated(testService("default", "a", ports), "6"))
	if _, err := r.EnsureLoadBalancer(context.Background(), "c", annotated(testService("default", "b", ports), "6"), testNodes("10.0.1.1")); err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("expected vnet 6 pool exhaustion, got %v", err)
	}
	if ip := ensureVia(t, r, testService("default", "c", ports)); ip != "10.5.0.100" {
		t.Errorf("default vnet pool unaffected, got %s", ip)
	}
}

func TestRouter_RejectsUnconfiguredOrInvalidVnet(t *testing.T) {
	r, f5, f6 := newTestRouter()
	for _, v := range []string{"7", "0", "abc", "", "5.0"} {
		svc := annotated(testService("default", "x", []v1.ServicePort{tcp(80, 30080)}), v)
		if _, err := r.EnsureLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
			t.Errorf("annotation %q: expected error", v)
		}
		if err := r.UpdateLoadBalancer(context.Background(), "c", svc, testNodes("10.0.1.1")); err == nil {
			t.Errorf("annotation %q: expected update error", v)
		}
	}
	if len(f5.rules)+len(f6.rules)+len(f5.addrs)+len(f6.addrs) != 0 {
		t.Error("nothing may be created for a rejected annotation")
	}
	_, err := r.EnsureLoadBalancer(context.Background(), "c", annotated(testService("default", "x", nil), "7"), nil)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("error should say the vnet is not configured: %v", err)
	}
}

// Changing the annotation moves the LB: the old vnet's rules and ipalias go away.
func TestRouter_AnnotationChangeMovesLoadBalancer(t *testing.T) {
	r, f5, f6 := newTestRouter()
	svc := testService("default", "web", []v1.ServicePort{tcp(80, 30080)})
	ensureVia(t, r, svc)
	if len(f5.rules) != 1 {
		t.Fatal("expected the LB on vnet 5")
	}

	moved := annotated(svc.DeepCopy(), "6")
	moved.Status.LoadBalancer = *lbStatus("10.5.0.100")
	if ip := ensureVia(t, r, moved); ip != "10.6.0.100" {
		t.Errorf("moved LB IP = %s", ip)
	}
	if len(f5.rules) != 0 || len(f5.addrs) != 0 {
		t.Errorf("old vnet must be cleaned: rules=%v addrs=%v", f5.rules, f5.addrs)
	}
	if len(f6.rules) != 1 || !f6.hasAddr("10.6.0.100") {
		t.Errorf("new vnet must hold the LB: rules=%v addrs=%v", f6.rules, f6.addrs)
	}

	// Update also converges (and cleans) after the move.
	if err := r.UpdateLoadBalancer(context.Background(), "c", moved, testNodes("10.0.1.1", "10.0.1.2")); err != nil {
		t.Fatal(err)
	}
	if got := f6.ruleFor(t, "80", "10.6.0.100").TargetIP; got != "10.0.1.1,10.0.1.2" {
		t.Errorf("target = %q", got)
	}
}

// A vnet's rules owned by someone else are never touched when cleaning up after a move.
func TestRouter_ReleaseElsewhereLeavesForeignObjects(t *testing.T) {
	r, f5, _ := newTestRouter()
	svc := annotated(testService("default", "web", []v1.ServicePort{tcp(80, 30080)}), "6")
	other := testService("default", "other", []v1.ServicePort{tcp(80, 30081)})
	ensureVia(t, r, other) // lands on vnet 5
	ensureVia(t, r, svc)   // vnet 6; cleanup scans vnet 5
	if len(f5.rules) != 1 || !f5.hasAddr("10.5.0.100") {
		t.Errorf("another Service's LB on vnet 5 must be untouched: rules=%v addrs=%v", f5.rules, f5.addrs)
	}
}

func TestRouter_DeleteAndGetSearchAllVnets(t *testing.T) {
	ctx := context.Background()
	r, f5, f6 := newTestRouter()
	svc := annotated(testService("default", "web", []v1.ServicePort{tcp(80, 30080)}), "6")
	ensureVia(t, r, svc)

	// The annotation is gone (or invalid) by the time of lookup/delete: still found.
	plain := testService("default", "web", svc.Spec.Ports)
	if st, ok, err := r.GetLoadBalancer(ctx, "c", plain); err != nil || !ok || st.Ingress[0].IP != "10.6.0.100" {
		t.Errorf("GetLoadBalancer = %v, %v, %v", st, ok, err)
	}
	bad := annotated(testService("default", "web", svc.Spec.Ports), "nope")
	if _, ok, err := r.GetLoadBalancer(ctx, "c", bad); err != nil || !ok {
		t.Errorf("GetLoadBalancer with invalid annotation = %v, %v", ok, err)
	}
	if err := r.EnsureLoadBalancerDeleted(ctx, "c", bad); err != nil {
		t.Fatal(err)
	}
	if len(f6.rules) != 0 || len(f6.addrs) != 0 || len(f5.rules) != 0 {
		t.Errorf("delete must clean every vnet: %v %v", f6.rules, f6.addrs)
	}
	if _, ok, _ := r.GetLoadBalancer(ctx, "c", svc); ok {
		t.Error("LB must be gone")
	}
}
