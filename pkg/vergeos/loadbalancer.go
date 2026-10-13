package vergeos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	vergeos "github.com/verge-io/govergeos"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	// AnnotationLoadBalancerIP requests a specific LB IP. It must be in the
	// configured pool and unused. Equivalent to spec.loadBalancerIP.
	AnnotationLoadBalancerIP = "loadbalancer.vergeos.io/ip"

	// ownerTag versions the ownership string stored in the `description` of
	// every rule and ipalias this CCM creates.
	ownerTag = "vergeos-ccm/v1"
)

type loadBalancer struct {
	rules   VNetRuleClient
	network NetworkClient
	addrs   VNetAddressClient
	config  lbConfig

	// mu serialises all LB operations: the service controller runs several
	// workers, and IP allocation must not race.
	mu sync.Mutex
	// applyPending is set when rules changed but ApplyRules did not succeed,
	// so the next call re-applies even if there is nothing left to diff.
	applyPending bool
}

func newLoadBalancer(rules VNetRuleClient, network NetworkClient, addrs VNetAddressClient, config lbConfig) *loadBalancer {
	return &loadBalancer{rules: rules, network: network, addrs: addrs, config: config}
}

// lbOwner identifies the Service a rule or ipalias belongs to.
type lbOwner struct {
	cluster, namespace, name, uid string
}

func newLBOwner(clusterName string, svc *v1.Service) lbOwner {
	return lbOwner{cluster: clusterName, namespace: svc.Namespace, name: svc.Name, uid: string(svc.UID)}
}

// String is the ownership marker stored in the description. Every field is
// query-escaped, so space and '=' can never appear inside a value and two
// different owners can never produce the same string. Ownership is decided
// by exact string equality.
func (o lbOwner) String() string {
	return fmt.Sprintf("%s cluster=%s namespace=%s name=%s uid=%s", ownerTag,
		url.QueryEscape(o.cluster), url.QueryEscape(o.namespace), url.QueryEscape(o.name), url.QueryEscape(o.uid))
}

// id is a short stable hash of the owner, used in rule and host names.
func (o lbOwner) id() string {
	sum := sha256.Sum256([]byte(o.String()))
	return hex.EncodeToString(sum[:])[:10]
}

// lbRuleName returns the VNet rule name for one service port:
// k8s-<owner hash>-<protocol>-<port>. Names are unique per vnet; the human
// readable owner is in the rule description.
func (o lbOwner) ruleName(protocol string, port int32) string {
	return fmt.Sprintf("k8s-%s-%s-%d", o.id(), protocol, port)
}

func (o lbOwner) hostname() string { return "k8s-lb-" + o.id() }

func legacyHostname(namespace, serviceName string) string {
	return fmt.Sprintf("k8s-lb-%s-%s", namespace, serviceName)
}

// statusIPs returns the LB IPs the Service already advertises.
func statusIPs(svc *v1.Service) []string {
	var ips []string
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			ips = append(ips, ing.IP)
		}
	}
	return ips
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// legacyRuleOwned reports whether r is an old-scheme rule that provably
// belongs to svc. The old name (k8s-<ns>-<svc>-<proto>-<port>) is ambiguous
// (a-b/c and a/b-c produce the same names), so a name match alone proves
// nothing. We require ALL of:
//   - empty description (never stamped),
//   - name exactly equals the legacy name of one of svc's current ports,
//   - destination IP is already in svc.status.loadBalancer.ingress,
//   - destination port and target port equal that port's Port and NodePort
//     (NodePorts are cluster-unique, so a lookalike Service from another
//     namespace or cluster will not match).
func legacyRuleOwned(r vergeos.VNetRule, svc *v1.Service, svcIPs []string) bool {
	if r.Description != "" || !contains(svcIPs, r.DestinationIP) {
		return false
	}
	for _, p := range svc.Spec.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = v1.ProtocolTCP
		}
		if proto != v1.ProtocolTCP && proto != v1.ProtocolUDP {
			continue
		}
		vp := protocolToVNetProtocol(proto)
		if r.Name == fmt.Sprintf("k8s-%s-%s-%s-%d", svc.Namespace, svc.Name, vp, p.Port) &&
			r.Protocol == vp &&
			r.DestinationPorts == strconv.Itoa(int(p.Port)) &&
			p.NodePort != 0 && r.TargetPorts == strconv.Itoa(int(p.NodePort)) {
			return true
		}
	}
	return false
}

// ruleOwned reports whether r provably belongs to the Service: exact owner
// marker, or a provable legacy rule (see legacyRuleOwned).
func ruleOwned(r vergeos.VNetRule, owner lbOwner, svc *v1.Service, svcIPs []string) bool {
	return r.Description == owner.String() || legacyRuleOwned(r, svc, svcIPs)
}

// ipsOf returns the destination IPs of the given rules.
func ipsOf(rules []vergeos.VNetRule) map[string]bool {
	m := map[string]bool{}
	for _, r := range rules {
		if r.DestinationIP != "" {
			m[r.DestinationIP] = true
		}
	}
	return m
}

// addrState classifies an ipalias relative to svc.
type addrState int

const (
	addrForeign addrState = iota // not provably ours
	addrExact                    // carries our exact marker
	addrLegacy                   // old-scheme alias, provable via an owned rule at the same IP
)

// classifyAddr: legacy aliases (empty description, legacy hostname) are only
// ours if one of the Service's owned rules already points at the same IP.
func classifyAddr(a *vergeos.VNetAddress, owner lbOwner, ip string, svc *v1.Service, ownedIPs map[string]bool) addrState {
	if a.Description == owner.String() {
		return addrExact
	}
	if a.Description == "" && a.Hostname == legacyHostname(svc.Namespace, svc.Name) && ownedIPs[ip] {
		return addrLegacy
	}
	return addrForeign
}

// listRules returns every rule on the vnet and the subset owned by svc,
// ordered by key so "first owned rule" is deterministic.
func (l *loadBalancer) listRules(ctx context.Context, owner lbOwner, svc *v1.Service) (all, owned []vergeos.VNetRule, err error) {
	all, err = l.rules.ListByNetwork(ctx, l.config.NetworkID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing VNet rules: %w", err)
	}
	svcIPs := statusIPs(svc)
	for _, r := range all {
		if ruleOwned(r, owner, svc, svcIPs) {
			owned = append(owned, r)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Key < owned[j].Key })
	return all, owned, nil
}

// requestedIP returns the IP requested via spec.loadBalancerIP or the
// annotation, or "" if none. Both set and different is an error.
func requestedIP(svc *v1.Service) (string, error) {
	spec := svc.Spec.LoadBalancerIP
	ann := svc.Annotations[AnnotationLoadBalancerIP]
	if spec != "" && ann != "" && spec != ann {
		return "", fmt.Errorf("spec.loadBalancerIP %q conflicts with annotation %s=%q", spec, AnnotationLoadBalancerIP, ann)
	}
	req := spec
	if req == "" {
		req = ann
	}
	if req == "" {
		return "", nil
	}
	ip := net.ParseIP(req)
	if ip == nil {
		return "", fmt.Errorf("requested load balancer IP %q is not a valid IP", req)
	}
	return ip.String(), nil
}

func (l *loadBalancer) inPool(ip string) bool {
	for _, p := range l.config.IPPool {
		if p == ip {
			return true
		}
		if pp := net.ParseIP(p); pp != nil && pp.String() == ip {
			return true
		}
	}
	return false
}

// ipAvailability describes whether ip can be used by svc.
type ipAvailability int

const (
	ipFree ipAvailability = iota // nobody uses it
	ipOurs                       // an ipalias we own already exists
	ipBusy                       // used by something else
)

func (l *loadBalancer) checkIP(ctx context.Context, ip string, all, owned []vergeos.VNetRule, owner lbOwner, svc *v1.Service) (ipAvailability, error) {
	ownedKeys := map[vergeos.FlexInt]bool{}
	for _, r := range owned {
		ownedKeys[r.Key] = true
	}
	for _, r := range all {
		if r.DestinationIP == ip && !ownedKeys[r.Key] {
			return ipBusy, nil
		}
	}
	ownedIPs := ipsOf(owned)
	if l.addrs == nil {
		return ipFree, nil
	}
	addr, err := l.addrs.GetByIP(ctx, l.config.NetworkID, ip)
	if vergeos.IsNotFoundError(err) {
		return ipFree, nil
	}
	if err != nil {
		return ipBusy, fmt.Errorf("checking VNet address for %s: %w", ip, err)
	}
	if addr != nil && classifyAddr(addr, owner, ip, svc, ownedIPs) != addrForeign {
		return ipOurs, nil
	}
	return ipBusy, nil
}

// chooseIP picks the LB IP for svc, in order:
//  1. the requested IP (spec.loadBalancerIP / annotation): must be in the pool and unused by others;
//  2. the IP the Service's existing rules already use;
//  3. an IP in status or an ipalias we own (left over from a failed attempt);
//  4. the first free pool IP.
func (l *loadBalancer) chooseIP(ctx context.Context, svc *v1.Service, owner lbOwner, all, owned []vergeos.VNetRule) (string, error) {
	req, err := requestedIP(svc)
	if err != nil {
		return "", err
	}
	if req != "" {
		if !l.inPool(req) {
			return "", fmt.Errorf("requested load balancer IP %s is not in the configured ipPool", req)
		}
		avail, err := l.checkIP(ctx, req, all, owned, owner, svc)
		if err != nil {
			return "", err
		}
		if avail == ipBusy {
			return "", fmt.Errorf("requested load balancer IP %s is already in use", req)
		}
		return req, nil
	}

	if len(owned) > 0 && owned[0].DestinationIP != "" {
		return owned[0].DestinationIP, nil
	}

	candidates := append([]string{}, statusIPs(svc)...)
	candidates = append(candidates, l.config.IPPool...)
	firstFree := ""
	for _, ip := range candidates {
		if !l.inPool(ip) {
			continue
		}
		avail, err := l.checkIP(ctx, ip, all, owned, owner, svc)
		if err != nil {
			return "", err
		}
		switch avail {
		case ipOurs:
			return ip, nil
		case ipFree:
			if firstFree == "" {
				firstFree = ip
			}
		}
	}
	if firstFree == "" {
		return "", fmt.Errorf("IP pool exhausted: all %d IPs are in use", len(l.config.IPPool))
	}
	return firstFree, nil
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

// sortedTargets returns the node IPs deduplicated and sorted, so the same
// node set always yields the same target string (no spurious updates).
func sortedTargets(nodes []*v1.Node) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range nodeAddresses(nodes) {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// sameIPSet compares comma-separated IP lists ignoring order.
func sameIPSet(a, b string) bool {
	split := func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		sort.Strings(out)
		return out
	}
	x, y := split(a), split(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
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

// desiredRule is the rule a service port should have.
type desiredRule struct {
	name, description, protocol              string
	destIP, destPorts, targetIP, targetPorts string
}

func (d desiredRule) key() string { return d.protocol + "/" + d.destPorts }

func buildDesired(owner lbOwner, svc *v1.Service, ip string, targets []string) ([]desiredRule, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("no node addresses available for LoadBalancer %s/%s", svc.Namespace, svc.Name)
	}
	targetIP := strings.Join(targets, ",")
	var out []desiredRule
	for _, p := range svc.Spec.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = v1.ProtocolTCP
		}
		if proto != v1.ProtocolTCP && proto != v1.ProtocolUDP {
			return nil, fmt.Errorf("service port %q: protocol %s is not supported (only TCP and UDP)", p.Name, proto)
		}
		if p.NodePort == 0 {
			return nil, fmt.Errorf("service port %d has no NodePort; allocateLoadBalancerNodePorts=false is not supported", p.Port)
		}
		vp := protocolToVNetProtocol(proto)
		out = append(out, desiredRule{
			name:        owner.ruleName(vp, p.Port),
			description: owner.String(),
			protocol:    vp,
			destIP:      ip,
			destPorts:   strconv.Itoa(int(p.Port)),
			targetIP:    targetIP,
			targetPorts: strconv.Itoa(int(p.NodePort)),
		})
	}
	return out, nil
}

func (d desiredRule) matches(r vergeos.VNetRule) bool {
	return r.Name == d.name && r.Description == d.description && r.Enabled &&
		r.Protocol == d.protocol && r.Direction == "incoming" && r.Action == "translate" &&
		r.DestinationIP == d.destIP && r.DestinationPorts == d.destPorts &&
		sameIPSet(r.TargetIP, d.targetIP) && r.TargetPorts == d.targetPorts
}

// applyRules activates rule changes. It also applies when an earlier apply
// failed (in-memory flag) or the vnet itself reports need_fw_apply (covers a
// CCM crash between writing rules and applying them).
func (l *loadBalancer) applyRules(ctx context.Context, changed bool) error {
	if !changed && !l.applyPending {
		net, err := l.network.Get(ctx, l.config.NetworkID)
		if err != nil {
			return fmt.Errorf("getting VNet %d: %w", l.config.NetworkID, err)
		}
		if net == nil || !net.NeedFWApply {
			return nil
		}
	}
	l.applyPending = true
	if err := l.network.ApplyRules(ctx, l.config.NetworkID); err != nil {
		return fmt.Errorf("applying VNet rules: %w", err)
	}
	l.applyPending = false
	return nil
}

// ensureAddress makes sure the ipalias for ip exists and is ours. A foreign
// alias is never claimed; a provable legacy alias is stamped with our marker.
func (l *loadBalancer) ensureAddress(ctx context.Context, ip string, owner lbOwner, svc *v1.Service, ownedIPs map[string]bool) error {
	if l.addrs == nil {
		return nil
	}
	addr, err := l.addrs.GetByIP(ctx, l.config.NetworkID, ip)
	if err == nil && addr != nil {
		switch classifyAddr(addr, owner, ip, svc, ownedIPs) {
		case addrExact:
			return nil
		case addrLegacy:
			if _, err := l.addrs.Update(ctx, int(addr.Key), &vergeos.VNetAddressUpdateRequest{
				Hostname:    strPtr(owner.hostname()),
				Description: strPtr(owner.String()),
			}); err != nil {
				return fmt.Errorf("adopting legacy VNet address for %s: %w", ip, err)
			}
			klog.Infof("Adopted legacy VNet address entry for %s", ip)
			return nil
		default:
			return fmt.Errorf("VNet address %s exists but is not owned by LoadBalancer %s/%s", ip, svc.Namespace, svc.Name)
		}
	}
	if err != nil && !vergeos.IsNotFoundError(err) {
		return fmt.Errorf("checking VNet address for %s: %w", ip, err)
	}
	if _, err := l.addrs.Create(ctx, &vergeos.VNetAddressCreateRequest{
		VNet:        l.config.NetworkID,
		IP:          ip,
		Type:        vergeos.AddressTypeIPAlias,
		Hostname:    owner.hostname(),
		Description: owner.String(),
	}); err != nil {
		return fmt.Errorf("creating VNet address for %s: %w", ip, err)
	}
	klog.Infof("Created VNet address entry for %s on network %d", ip, l.config.NetworkID)
	return nil
}

// deleteOwnedAddresses removes the ipalias for each ip, but only if we own it.
func (l *loadBalancer) deleteOwnedAddresses(ctx context.Context, ips []string, owner lbOwner, svc *v1.Service, ownedIPs map[string]bool) error {
	if l.addrs == nil {
		return nil
	}
	var errs []error
	seen := map[string]bool{}
	for _, ip := range ips {
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		addr, err := l.addrs.GetByIP(ctx, l.config.NetworkID, ip)
		if vergeos.IsNotFoundError(err) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("finding VNet address for %s: %w", ip, err))
			continue
		}
		if addr == nil || classifyAddr(addr, owner, ip, svc, ownedIPs) == addrForeign {
			continue
		}
		if err := l.addrs.Delete(ctx, int(addr.Key)); err != nil && !vergeos.IsNotFoundError(err) {
			errs = append(errs, fmt.Errorf("deleting VNet address for %s: %w", ip, err))
			continue
		}
		klog.Infof("Deleted VNet address entry for %s", ip)
	}
	return errors.Join(errs...)
}

// reconcileRules creates, updates and deletes rules so that the owned set
// equals desired. It attempts every operation and returns the joined errors;
// changed reports whether anything was modified (so rules need applying).
func (l *loadBalancer) reconcileRules(ctx context.Context, all, owned []vergeos.VNetRule, desired []desiredRule) (changed bool, err error) {
	ownedKeys := map[vergeos.FlexInt]bool{}
	for _, r := range owned {
		ownedKeys[r.Key] = true
	}
	foreignNames := map[string]bool{}
	for _, r := range all {
		if !ownedKeys[r.Key] {
			foreignNames[r.Name] = true
		}
	}
	for _, d := range desired {
		if foreignNames[d.name] {
			return false, fmt.Errorf("VNet rule name %q is already used by a rule this CCM does not own", d.name)
		}
	}

	byKey := map[string]desiredRule{}
	for _, d := range desired {
		byKey[d.key()] = d
	}
	sorted := append([]vergeos.VNetRule{}, owned...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	matched := map[string]bool{}
	var del []vergeos.VNetRule
	type upd struct {
		rule vergeos.VNetRule
		want desiredRule
	}
	var updates []upd
	for _, r := range sorted {
		k := r.Protocol + "/" + r.DestinationPorts
		d, ok := byKey[k]
		if !ok || matched[k] {
			del = append(del, r)
			continue
		}
		matched[k] = true
		if !d.matches(r) {
			updates = append(updates, upd{r, d})
		}
	}

	var errs []error
	for _, r := range del {
		if err := l.rules.Delete(ctx, int(r.Key)); err != nil && !vergeos.IsNotFoundError(err) {
			errs = append(errs, fmt.Errorf("deleting VNet rule %q: %w", r.Name, err))
			continue
		}
		changed = true
		klog.Infof("Deleted VNet rule %q", r.Name)
	}
	for _, u := range updates {
		d := u.want
		enabled := true
		if _, err := l.rules.Update(ctx, int(u.rule.Key), &vergeos.VNetRuleUpdateRequest{
			Name:             &d.name,
			Description:      &d.description,
			Enabled:          &enabled,
			Protocol:         &d.protocol,
			Direction:        strPtr("incoming"),
			Action:           strPtr("translate"),
			DestinationIP:    &d.destIP,
			DestinationPorts: &d.destPorts,
			TargetIP:         &d.targetIP,
			TargetPorts:      &d.targetPorts,
		}); err != nil {
			errs = append(errs, fmt.Errorf("updating VNet rule %q: %w", u.rule.Name, err))
			continue
		}
		changed = true
		klog.Infof("Updated VNet rule %q (%s:%s -> %s:%s)", d.name, d.destIP, d.destPorts, d.targetIP, d.targetPorts)
	}
	for _, d := range desired {
		if matched[d.key()] {
			continue
		}
		enabled := true
		if _, err := l.rules.Create(ctx, &vergeos.VNetRuleCreateRequest{
			VNet:             l.config.NetworkID,
			Name:             d.name,
			Description:      d.description,
			Enabled:          &enabled,
			Protocol:         &d.protocol,
			Direction:        strPtr("incoming"),
			Action:           strPtr("translate"),
			DestinationIP:    &d.destIP,
			DestinationPorts: &d.destPorts,
			TargetIP:         &d.targetIP,
			TargetPorts:      &d.targetPorts,
		}); err != nil {
			errs = append(errs, fmt.Errorf("creating VNet rule %q: %w", d.name, err))
			continue
		}
		changed = true
		klog.Infof("Created VNet rule %q (%s:%s -> %s:%s)", d.name, d.destIP, d.destPorts, d.targetIP, d.targetPorts)
	}
	return changed, errors.Join(errs...)
}

func (l *loadBalancer) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (*v1.LoadBalancerStatus, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	_, owned, err := l.listRules(ctx, newLBOwner(clusterName, service), service)
	if err != nil {
		return nil, false, err
	}
	if len(owned) == 0 {
		return nil, false, nil
	}
	return lbStatus(owned[0].DestinationIP), true, nil
}

func lbStatus(ip string) *v1.LoadBalancerStatus {
	return &v1.LoadBalancerStatus{Ingress: []v1.LoadBalancerIngress{{IP: ip}}}
}

func (l *loadBalancer) GetLoadBalancerName(ctx context.Context, clusterName string, service *v1.Service) string {
	return fmt.Sprintf("k8s-%s-%s", service.Namespace, service.Name)
}

// EnsureLoadBalancer converges the VNet to the Service: one translate rule per
// port, all pointing at the current node set, on a single stable IP. Any
// failure is returned so the service controller retries; the next call
// resumes from whatever state the failed one left behind.
func (l *loadBalancer) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ip, err := l.converge(ctx, clusterName, service, nodes)
	if err != nil {
		return nil, err
	}
	return lbStatus(ip), nil
}

func (l *loadBalancer) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) error {
	if service.DeletionTimestamp != nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.converge(ctx, clusterName, service, nodes)
	return err
}

// converge is the shared body of Ensure and Update. Caller holds l.mu.
func (l *loadBalancer) converge(ctx context.Context, clusterName string, svc *v1.Service, nodes []*v1.Node) (string, error) {
	owner := newLBOwner(clusterName, svc)

	all, owned, err := l.listRules(ctx, owner, svc)
	if err != nil {
		return "", err
	}
	ip, err := l.chooseIP(ctx, svc, owner, all, owned)
	if err != nil {
		return "", err
	}
	desired, err := buildDesired(owner, svc, ip, sortedTargets(nodes))
	if err != nil {
		return "", err
	}

	if err := l.ensureAddress(ctx, ip, owner, svc, ipsOf(owned)); err != nil {
		return "", err
	}

	changed, err := l.reconcileRules(ctx, all, owned, desired)
	// Apply whatever did change, even on partial failure, so the VNet is
	// never left with rules written but not active.
	applyErr := l.applyRules(ctx, changed)
	if err := errors.Join(err, applyErr); err != nil {
		return "", err
	}

	// The IP moved (e.g. loadBalancerIP changed): release the old ipalias.
	var oldIPs []string
	for _, r := range owned {
		if r.DestinationIP != ip {
			oldIPs = append(oldIPs, r.DestinationIP)
		}
	}
	for _, s := range statusIPs(svc) {
		if s != ip {
			oldIPs = append(oldIPs, s)
		}
	}
	if err := l.deleteOwnedAddresses(ctx, oldIPs, owner, svc, ipsOf(owned)); err != nil {
		return "", err
	}
	klog.Infof("LoadBalancer %s/%s converged at %s", svc.Namespace, svc.Name, ip)
	return ip, nil
}

func (l *loadBalancer) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, service *v1.Service) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	owner := newLBOwner(clusterName, service)
	_, owned, err := l.listRules(ctx, owner, service)
	if err != nil {
		return err
	}

	var errs []error
	changed := false
	ownedIPs := ipsOf(owned)
	ips := statusIPs(service)
	// An ipalias can be orphaned by a failed Ensure (alias is created before
	// the rules), leaving no rule or status IP to find it by. Probe the whole
	// pool; only aliases carrying our exact marker are removed.
	ips = append(ips, l.config.IPPool...)
	if req, err := requestedIP(service); err == nil && req != "" {
		ips = append(ips, req)
	}
	for _, r := range owned {
		ips = append(ips, r.DestinationIP)
		if err := l.rules.Delete(ctx, int(r.Key)); err != nil && !vergeos.IsNotFoundError(err) {
			errs = append(errs, fmt.Errorf("deleting VNet rule %q: %w", r.Name, err))
			continue
		}
		changed = true
		klog.Infof("Deleted VNet rule %q", r.Name)
	}
	if err := l.applyRules(ctx, changed); err != nil {
		errs = append(errs, err)
	}
	// Only release the IP once every rule is gone.
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return l.deleteOwnedAddresses(ctx, ips, owner, service, ownedIPs)
}
