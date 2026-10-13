package vergeos

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
)

type cloudConfig struct {
	// NodeNetworkID, when set, limits node InternalIPs taken from NIC DHCP leases
	// to NICs attached to this VergeOS vnet.
	NodeNetworkID int      `json:"nodeNetworkID"`
	LoadBalancer  lbConfig `json:"loadBalancer"`
}

type lbConfig struct {
	Enabled   bool     `json:"enabled"`
	NetworkID int      `json:"networkID"`
	IPPool    []string `json:"ipPool"`
}

func parseConfig(r io.Reader) (*cloudConfig, error) {
	cfg := &cloudConfig{}
	if r == nil {
		return cfg, nil
	}

	if err := json.NewDecoder(r).Decode(cfg); err != nil {
		return nil, fmt.Errorf("parsing cloud config: %w", err)
	}

	if cfg.NodeNetworkID < 0 {
		return nil, fmt.Errorf("nodeNetworkID must not be negative")
	}

	if cfg.LoadBalancer.Enabled {
		if cfg.LoadBalancer.NetworkID == 0 {
			return nil, fmt.Errorf("loadBalancer.networkID is required when loadBalancer is enabled")
		}
		if len(cfg.LoadBalancer.IPPool) == 0 {
			return nil, fmt.Errorf("loadBalancer.ipPool must contain at least one IP when loadBalancer is enabled")
		}
		pool, err := validateIPPool(cfg.LoadBalancer.IPPool)
		if err != nil {
			return nil, err
		}
		cfg.LoadBalancer.IPPool = pool
	}

	return cfg, nil
}

// validateIPPool requires every entry to be a single IPv4 address (no CIDRs,
// ranges, hostnames, quotes or whitespace) and rejects duplicates. Entries
// are returned in canonical dotted-quad form. Pool values end up in VergeOS
// filter strings and rule fields, so garbage must never get past here.
func validateIPPool(pool []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(pool))
	for i, e := range pool {
		ip := net.ParseIP(e)
		if ip == nil || ip.To4() == nil || strings.Contains(e, ":") {
			return nil, fmt.Errorf("loadBalancer.ipPool[%d]: %q is not a single IPv4 address (CIDRs and ranges are not supported)", i, e)
		}
		c := ip.String()
		if seen[c] {
			return nil, fmt.Errorf("loadBalancer.ipPool[%d]: duplicate address %s", i, c)
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}
