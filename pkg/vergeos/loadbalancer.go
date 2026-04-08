package vergeos

import (
	"context"
	"fmt"
	"strings"

	vergeos "github.com/verge-io/govergeos"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

type loadBalancer struct {
	rules   VNetRuleClient
	network NetworkClient
	addrs   VNetAddressClient
	config  lbConfig
}

func newLoadBalancer(rules VNetRuleClient, network NetworkClient, addrs VNetAddressClient, config lbConfig) *loadBalancer {
	return &loadBalancer{rules: rules, network: network, addrs: addrs, config: config}
}

// lbRuleName returns the VNet rule name for a specific service port.
// Format: k8s-<namespace>-<serviceName>-<protocol>-<port>
func lbRuleName(namespace, serviceName string, protocol v1.Protocol, port int32) string {
	return fmt.Sprintf("k8s-%s-%s-%s-%d", namespace, serviceName, strings.ToLower(string(protocol)), port)
}

// lbRulePrefix returns the naming prefix for all rules belonging to a service.
// Used to find all rules for a service regardless of port.
func lbRulePrefix(namespace, serviceName string) string {
	return fmt.Sprintf("k8s-%s-%s-", namespace, serviceName)
}

// findServiceRules returns all VNet rules belonging to a service.
func (l *loadBalancer) findServiceRules(ctx context.Context, namespace, serviceName string) ([]vergeos.VNetRule, error) {
	allRules, err := l.rules.ListByNetwork(ctx, l.config.NetworkID)
	if err != nil {
		return nil, fmt.Errorf("listing VNet rules: %w", err)
	}

	prefix := lbRulePrefix(namespace, serviceName)
	var matching []vergeos.VNetRule
	for _, r := range allRules {
		if strings.HasPrefix(r.Name, prefix) {
			matching = append(matching, r)
		}
	}
	return matching, nil
}

// allocateIP finds the first unused IP from the configured pool.
func (l *loadBalancer) allocateIP(ctx context.Context) (string, error) {
	allRules, err := l.rules.ListByNetwork(ctx, l.config.NetworkID)
	if err != nil {
		return "", fmt.Errorf("listing VNet rules for IP allocation: %w", err)
	}

	// Collect all IPs currently in use by k8s LB rules.
	usedIPs := map[string]bool{}
	for _, r := range allRules {
		if strings.HasPrefix(r.Name, "k8s-") && r.DestinationIP != "" {
			usedIPs[r.DestinationIP] = true
		}
	}

	for _, ip := range l.config.IPPool {
		if !usedIPs[ip] {
			return ip, nil
		}
	}

	return "", fmt.Errorf("IP pool exhausted: all %d IPs are in use", len(l.config.IPPool))
}

// nodeAddresses extracts internal IPs from a list of nodes.
func nodeAddresses(nodes []*v1.Node) []string {
	var addrs []string
	for _, n := range nodes {
		for _, addr := range n.Status.Addresses {
			if addr.Type == v1.NodeInternalIP {
				addrs = append(addrs, addr.Address)
				break // One IP per node is enough.
			}
		}
	}
	return addrs
}

// protocolToVNetProtocol maps K8s protocol to VergeOS VNet rule protocol.
func protocolToVNetProtocol(p v1.Protocol) string {
	switch p {
	case v1.ProtocolTCP:
		return "tcp"
	case v1.ProtocolUDP:
		return "udp"
	default:
		klog.Warningf("Unsupported protocol %q, defaulting to tcp", p)
		return "tcp"
	}
}

// strPtr returns a pointer to a string.
func strPtr(s string) *string { return &s }

func (l *loadBalancer) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (*v1.LoadBalancerStatus, bool, error) {
	rules, err := l.findServiceRules(ctx, service.Namespace, service.Name)
	if err != nil {
		return nil, false, err
	}
	if len(rules) == 0 {
		return nil, false, nil
	}

	// All rules for a service share the same destination IP.
	ip := rules[0].DestinationIP
	return &v1.LoadBalancerStatus{
		Ingress: []v1.LoadBalancerIngress{{IP: ip}},
	}, true, nil
}

func (l *loadBalancer) GetLoadBalancerName(ctx context.Context, clusterName string, service *v1.Service) string {
	return fmt.Sprintf("k8s-%s-%s", service.Namespace, service.Name)
}

func (l *loadBalancer) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	// Check if LB already exists (idempotent).
	existing, err := l.findServiceRules(ctx, service.Namespace, service.Name)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		ip := existing[0].DestinationIP
		klog.Infof("LoadBalancer for %s/%s already exists at %s", service.Namespace, service.Name, ip)
		return &v1.LoadBalancerStatus{
			Ingress: []v1.LoadBalancerIngress{{IP: ip}},
		}, nil
	}

	// Allocate an IP.
	ip, err := l.allocateIP(ctx)
	if err != nil {
		return nil, err
	}
	klog.Infof("Allocated IP %s for LoadBalancer %s/%s", ip, service.Namespace, service.Name)

	// Create VNet address entry so the network owns this IP.
	if l.addrs != nil {
		// Idempotent: check if already exists.
		_, err := l.addrs.GetByIP(ctx, l.config.NetworkID, ip)
		if vergeos.IsNotFoundError(err) {
			hostname := fmt.Sprintf("k8s-lb-%s-%s", service.Namespace, service.Name)
			_, err = l.addrs.Create(ctx, &vergeos.VNetAddressCreateRequest{
				VNet:     l.config.NetworkID,
				IP:       ip,
				Type:     "ipalias",
				Hostname: hostname,
			})
			if err != nil {
				return nil, fmt.Errorf("creating VNet address for %s: %w", ip, err)
			}
			klog.Infof("Created VNet address entry for %s on network %d", ip, l.config.NetworkID)
		} else if err != nil {
			return nil, fmt.Errorf("checking VNet address for %s: %w", ip, err)
		}
	}

	// Build target: comma-separated node IPs.
	addrs := nodeAddresses(nodes)
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no node addresses available for LoadBalancer %s/%s", service.Namespace, service.Name)
	}
	targetIP := strings.Join(addrs, ",")

	// Create a VNet rule for each service port.
	enabled := true
	networkID := l.config.NetworkID
	for _, port := range service.Spec.Ports {
		ruleName := lbRuleName(service.Namespace, service.Name, port.Protocol, port.Port)
		protocol := protocolToVNetProtocol(port.Protocol)
		destPort := fmt.Sprintf("%d", port.Port)
		targetPort := fmt.Sprintf("%d", port.NodePort)

		_, err := l.rules.Create(ctx, &vergeos.VNetRuleCreateRequest{
			VNet:             networkID,
			Name:             ruleName,
			Enabled:          &enabled,
			Protocol:         &protocol,
			Direction:        strPtr("incoming"),
			Action:           strPtr("translate"),
			DestinationIP:    &ip,
			DestinationPorts: &destPort,
			TargetIP:         &targetIP,
			TargetPorts:      &targetPort,
		})
		if err != nil {
			return nil, fmt.Errorf("creating VNet rule %q: %w", ruleName, err)
		}
		klog.Infof("Created VNet rule %q (%s:%s -> %s:%s)", ruleName, ip, destPort, targetIP, targetPort)
	}

	// Apply rules to activate.
	if err := l.network.ApplyRules(ctx, networkID); err != nil {
		return nil, fmt.Errorf("applying VNet rules: %w", err)
	}

	return &v1.LoadBalancerStatus{
		Ingress: []v1.LoadBalancerIngress{{IP: ip}},
	}, nil
}

func (l *loadBalancer) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) error {
	rules, err := l.findServiceRules(ctx, service.Namespace, service.Name)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		return nil // Nothing to update.
	}

	addrs := nodeAddresses(nodes)
	if len(addrs) == 0 {
		return fmt.Errorf("no node addresses available for LoadBalancer %s/%s", service.Namespace, service.Name)
	}
	targetIP := strings.Join(addrs, ",")

	for _, r := range rules {
		_, err := l.rules.Update(ctx, int(r.Key), &vergeos.VNetRuleUpdateRequest{
			TargetIP: &targetIP,
		})
		if err != nil {
			return fmt.Errorf("updating VNet rule %q: %w", r.Name, err)
		}
		klog.Infof("Updated VNet rule %q target to %s", r.Name, targetIP)
	}

	if err := l.network.ApplyRules(ctx, l.config.NetworkID); err != nil {
		return fmt.Errorf("applying VNet rules: %w", err)
	}

	return nil
}

func (l *loadBalancer) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, service *v1.Service) error {
	rules, err := l.findServiceRules(ctx, service.Namespace, service.Name)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		return nil // Idempotent.
	}

	// Capture the LB IP before deleting rules (all rules share the same destination IP).
	lbIP := rules[0].DestinationIP

	for _, r := range rules {
		if err := l.rules.Delete(ctx, int(r.Key)); err != nil {
			if !vergeos.IsNotFoundError(err) {
				return fmt.Errorf("deleting VNet rule %q: %w", r.Name, err)
			}
		}
		klog.Infof("Deleted VNet rule %q", r.Name)
	}

	if err := l.network.ApplyRules(ctx, l.config.NetworkID); err != nil {
		return fmt.Errorf("applying VNet rules: %w", err)
	}

	// Remove the VNet address entry for the LB IP.
	if l.addrs != nil && lbIP != "" {
		addr, err := l.addrs.GetByIP(ctx, l.config.NetworkID, lbIP)
		if err != nil && !vergeos.IsNotFoundError(err) {
			klog.Warningf("Failed to find VNet address for %s: %v", lbIP, err)
		} else if addr != nil {
			if err := l.addrs.Delete(ctx, int(addr.Key)); err != nil {
				klog.Warningf("Failed to delete VNet address for %s: %v", lbIP, err)
			} else {
				klog.Infof("Deleted VNet address entry for %s", lbIP)
			}
		}
	}

	return nil
}
