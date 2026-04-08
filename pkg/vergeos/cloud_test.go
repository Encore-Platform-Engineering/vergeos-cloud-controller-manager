package vergeos

import (
	"strings"
	"testing"

	cloudprovider "k8s.io/cloud-provider"
)

func TestCloudProviderName(t *testing.T) {
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ProviderName() != ProviderName {
		t.Errorf("expected provider name %q, got %q", ProviderName, c.ProviderName())
	}
}

func TestCloudImplementsInterface(t *testing.T) {
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify cloud implements cloudprovider.Interface at compile time.
	var _ cloudprovider.Interface = c
}

func TestCloudInstancesV2(t *testing.T) {
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inst, ok := c.InstancesV2()
	if !ok {
		t.Error("expected InstancesV2 to be supported")
	}
	if inst == nil {
		t.Error("expected InstancesV2 to be non-nil")
	}
}

func TestCloudLoadBalancer_Enabled(t *testing.T) {
	cfg := `{"loadBalancer":{"enabled":true,"networkID":5,"ipPool":["10.0.0.1"]}}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lb, ok := c.LoadBalancer()
	if !ok {
		t.Error("expected LoadBalancer to be supported when enabled")
	}
	if lb == nil {
		t.Error("expected LoadBalancer to be non-nil")
	}
}

func TestCloudLoadBalancer_Disabled(t *testing.T) {
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lb, ok := c.LoadBalancer()
	if ok {
		t.Error("expected LoadBalancer to not be supported when disabled")
	}
	if lb != nil {
		t.Error("expected LoadBalancer to be nil when disabled")
	}
}

func TestCloudUnsupportedInterfaces(t *testing.T) {
	cfg := `{}`
	c, err := newCloud(strings.NewReader(cfg))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := c.Instances(); ok {
		t.Error("Instances should not be supported (deprecated)")
	}
	if _, ok := c.Zones(); ok {
		t.Error("Zones should not be supported (deprecated)")
	}
	if _, ok := c.Routes(); ok {
		t.Error("Routes should not be supported")
	}
	if _, ok := c.Clusters(); ok {
		t.Error("Clusters should not be supported")
	}
}
