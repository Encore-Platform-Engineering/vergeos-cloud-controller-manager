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
			"ipPool": ["192.0.2.100", "192.0.2.101"]
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
	if cfg.LoadBalancer.IPPool[0] != "192.0.2.100" {
		t.Errorf("expected first IP=192.0.2.100, got %s", cfg.LoadBalancer.IPPool[0])
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
			"ipPool": ["192.0.2.100"]
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

func TestParseConfig_NodeNetworkID(t *testing.T) {
	cfg, err := parseConfig(strings.NewReader(`{"nodeNetworkID": 7}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeNetworkID != 7 {
		t.Errorf("expected 7, got %d", cfg.NodeNetworkID)
	}
	if _, err := parseConfig(strings.NewReader(`{"nodeNetworkID": -1}`)); err == nil {
		t.Error("expected error for negative nodeNetworkID")
	}
}

func TestParseConfig_IPPoolValidation(t *testing.T) {
	cfgWith := func(pool string) string {
		return `{"loadBalancer":{"enabled":true,"networkID":5,"ipPool":` + pool + `}}`
	}
	for name, pool := range map[string]string{
		"cidr":      `["10.0.0.0/24"]`,
		"range":     `["10.0.0.1-10.0.0.9"]`,
		"garbage":   `["not-an-ip"]`,
		"quote":     `["10.0.0.1' or ip ne '"]`,
		"empty":     `[""]`,
		"space":     `[" 10.0.0.1"]`,
		"ipv6":      `["fd00::1"]`,
		"mapped":    `["::ffff:10.0.0.1"]`,
		"duplicate": `["10.0.0.1","10.0.0.2","10.0.0.1"]`,
		"leading0":  `["010.000.000.001","10.0.0.1"]`,
	} {
		if _, err := parseConfig(strings.NewReader(cfgWith(pool))); err == nil {
			t.Errorf("%s: expected error for ipPool %s", name, pool)
		}
	}

	cfg, err := parseConfig(strings.NewReader(cfgWith(`["10.0.0.1","10.0.0.2"]`)))
	if err != nil || len(cfg.LoadBalancer.IPPool) != 2 {
		t.Fatalf("valid pool rejected: %v", err)
	}

	// Disabled LB: pool is not inspected.
	if _, err := parseConfig(strings.NewReader(`{"loadBalancer":{"enabled":false,"ipPool":["junk"]}}`)); err != nil {
		t.Errorf("disabled LB must not validate pool: %v", err)
	}
}

func TestParseConfig_SourceNATAndAccept(t *testing.T) {
	cfg, err := parseConfig(strings.NewReader(`{"loadBalancer":{"enabled":true,"networkID":5,"ipPool":["192.0.2.100"],"sourceNATTarget":"192.0.2.1","acceptRules":true}}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LoadBalancer.SourceNATTarget != "192.0.2.1" || !cfg.LoadBalancer.AcceptRules {
		t.Errorf("sourceNATTarget/acceptRules not parsed: %+v", cfg.LoadBalancer)
	}
	for _, bad := range []string{`"a b"`, `"x\"y"`, `"1.2.3.4;"`} {
		in := `{"loadBalancer":{"enabled":true,"networkID":5,"ipPool":["192.0.2.100"],"sourceNATTarget":` + bad + `}}`
		if _, err := parseConfig(strings.NewReader(in)); err == nil {
			t.Errorf("sourceNATTarget %s: expected error", bad)
		}
	}
}
