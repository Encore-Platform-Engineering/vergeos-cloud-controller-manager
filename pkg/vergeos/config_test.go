package vergeos

import (
	"strings"
	"testing"
)

func TestParseConfig_Valid(t *testing.T) {
	input := `{
		"loadBalancer": {
			"enabled": true,
			"networkID": 5,
			"ipPool": ["10.8.8.100", "10.8.8.101"]
		}
	}`

	cfg, err := parseConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.LoadBalancer.Enabled {
		t.Error("expected loadBalancer.enabled = true")
	}
	if cfg.LoadBalancer.NetworkID != 5 {
		t.Errorf("expected networkID=5, got %d", cfg.LoadBalancer.NetworkID)
	}
	if len(cfg.LoadBalancer.IPPool) != 2 {
		t.Errorf("expected 2 IPs in pool, got %d", len(cfg.LoadBalancer.IPPool))
	}
	if cfg.LoadBalancer.IPPool[0] != "10.8.8.100" {
		t.Errorf("expected first IP=10.8.8.100, got %s", cfg.LoadBalancer.IPPool[0])
	}
}

func TestParseConfig_NilReader(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LoadBalancer.Enabled {
		t.Error("expected loadBalancer.enabled = false with nil reader")
	}
}

func TestParseConfig_EmptyJSON(t *testing.T) {
	cfg, err := parseConfig(strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LoadBalancer.Enabled {
		t.Error("expected loadBalancer.enabled = false")
	}
}

func TestParseConfig_InvalidJSON(t *testing.T) {
	_, err := parseConfig(strings.NewReader("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseConfig_LBEnabledMissingNetworkID(t *testing.T) {
	input := `{
		"loadBalancer": {
			"enabled": true,
			"ipPool": ["10.8.8.100"]
		}
	}`
	_, err := parseConfig(strings.NewReader(input))
	if err == nil {
		t.Error("expected error when LB enabled but networkID missing")
	}
}

func TestParseConfig_LBEnabledEmptyPool(t *testing.T) {
	input := `{
		"loadBalancer": {
			"enabled": true,
			"networkID": 5,
			"ipPool": []
		}
	}`
	_, err := parseConfig(strings.NewReader(input))
	if err == nil {
		t.Error("expected error when LB enabled but ipPool empty")
	}
}
