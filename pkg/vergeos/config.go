package vergeos

import (
	"encoding/json"
	"fmt"
	"io"
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
	}

	return cfg, nil
}
