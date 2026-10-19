package vergeos

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
)

type cloudConfig struct {
	// NodeNetworkID, when set, limits node InternalIPs (NIC DHCP leases and
	// guest-agent IPs matched to a NIC by MAC) to NICs attached to this VergeOS vnet.
	NodeNetworkID int `json:"nodeNetworkID"`
	// CAFile is a PEM file with extra CA certificates to trust for the VergeOS
	// API (private/self-signed CA). Falls back to $VERGEOS_CA_FILE.
	CAFile       string   `json:"caFile"`
	LoadBalancer lbConfig `json:"loadBalancer"`
}

type lbConfig struct {
	Enabled   bool     `json:"enabled"`
	NetworkID int      `json:"networkID"`
	IPPool    []string `json:"ipPool"`
	// SourceNATTarget, when set, adds one outgoing `translate` (source NAT)
	// rule per Service port whose target is this value (a VergeOS filter
	// token or IP such as the LB vnet's router address). It makes the
	// backend replies return through the LB vnet, which same-vnet (hairpin)
	// and routed clients need. Empty (default) = no SNAT rules.
	SourceNATTarget string `json:"sourceNATTarget"`
	// AcceptRules adds one incoming `accept` rule per Service port for the
	// LB IP, for vnets whose firewall drops traffic that no rule accepts.
	AcceptRules bool `json:"acceptRules"`
	// ApplyStagedChanges makes the CCM call ApplyRules whenever the vnet
	// reports need_fw_apply, even if this CCM changed nothing. That recovers
	// from a crash between writing rules and applying them, but also
	// activates other people's staged firewall edits on the vnet. Default off:
	// the CCM only applies after its own rule changes.
	ApplyStagedChanges bool `json:"applyStagedChanges"`
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
	if t := cfg.LoadBalancer.SourceNATTarget; t != "" && !sourceNATTargetRE.MatchString(t) {
		return nil, fmt.Errorf("loadBalancer.sourceNATTarget %q must be an IP or a VergeOS filter token (letters, digits and . : / _ - only)", t)
	}

	return cfg, nil
}

// sourceNATTargetRE limits sourceNATTarget to characters that can appear in an
// IP/CIDR or a VergeOS filter token: the value is sent verbatim as a rule's
// target_ip.
var sourceNATTargetRE = regexp.MustCompile(`^[A-Za-z0-9._:/-]+$`)

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
