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

// ipsOf returns the LB IPs the given rules point at. Outgoing (SNAT) rules
// are skipped: their destination is the node set, not an LB IP.
func ipsOf(rules []vergeos.VNetRule) map[string]bool {
	m := map[string]bool{}
	for _, r := range rules {
		if r.Direction != "outgoing" && r.DestinationIP != "" {
			m[r.DestinationIP] = true
		}
	}
	return m
}

// vipIP returns the LB IP of the first (lowest key) incoming rule, or "".
func vipIP(rules []vergeos.VNetRule) string {
	for _, r := range rules {
		if r.Direction != "outgoing" && r.DestinationIP != "" {
			return r.DestinationIP
		}
	}
	return ""
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

// addressesByIP lists every address on the vnet once, grouped by IP, so IP
// selection costs one API call instead of two per candidate.
func (l *loadBalancer) addressesByIP(ctx context.Context) (map[string][]vergeos.VNetAddress, error) {
	byIP := map[string][]vergeos.VNetAddress{}
	if l.addrs == nil {
		return byIP, nil
	}
	list, err := l.addrs.ListByNetwork(ctx, l.config.NetworkID)
	if err != nil {
		return nil, fmt.Errorf("listing VNet addresses: %w", err)
	}
	for _, a := range list {
		byIP[a.IP] = append(byIP[a.IP], a)
	}
	return byIP, nil
}

func (l *loadBalancer) checkIP(ip string, byIP map[string][]vergeos.VNetAddress, all, owned []vergeos.VNetRule, owner lbOwner, svc *v1.Service) ipAvailability {
	ownedKeys := map[vergeos.FlexInt]bool{}
	for _, r := range owned {
		ownedKeys[r.Key] = true
	}
	for _, r := range all {
		if r.DestinationIP == ip && !ownedKeys[r.Key] {
			return ipBusy
		}
	}
	ownedIPs := ipsOf(owned)
	avail := ipFree
	for i := range byIP[ip] {
		if classifyAddr(&byIP[ip][i], owner, ip, svc, ownedIPs) == addrForeign {
			return ipBusy
		}
		avail = ipOurs
	}
	return avail
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
	if existing := vipIP(owned); req == "" && existing != "" {
		return existing, nil
	}
	if req != "" && !l.inPool(req) {
		return "", fmt.Errorf("requested load balancer IP %s is not in the configured ipPool", req)
	}
	byIP, err := l.addressesByIP(ctx)
	if err != nil {
		return "", err
	}
	if req != "" {
		if l.checkIP(req, byIP, all, owned, owner, svc) == ipBusy {
			return "", fmt.Errorf("requested load balancer IP %s is already in use", req)
		}
		return req, nil
	}

	candidates := append([]string{}, statusIPs(svc)...)
	candidates = append(candidates, l.config.IPPool...)
	firstFree := ""
	for _, ip := range candidates {
		if !l.inPool(ip) {
			continue
		}
		switch l.checkIP(ip, byIP, all, owned, owner, svc) {
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

func sourceIPPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nilIfEmpty returns nil for "", so unset optional fields are omitted from
// create requests.
func nilIfEmpty(s string) *string { return sourceIPPtr(s) }

// annotationSourceRanges is the legacy equivalent of
// spec.loadBalancerSourceRanges (comma-separated CIDRs).
const annotationSourceRanges = "service.beta.kubernetes.io/load-balancer-source-ranges"

// validateService rejects Services this CCM cannot serve correctly, so they
// fail loudly (Service event) instead of being half-served.
func validateService(svc *v1.Service) error {
	// VergeOS translate rules have no health check, so traffic is spread over
	// every node's NodePort; with Local, nodes without a local endpoint would
	// drop 1/N of connections (and the client IP is not preserved).
	if svc.Spec.ExternalTrafficPolicy == v1.ServiceExternalTrafficPolicyLocal {
		return fmt.Errorf("service %s/%s: externalTrafficPolicy=Local (healthCheckNodePort) is not supported by the VergeOS load balancer; use Cluster", svc.Namespace, svc.Name)
	}
	return nil
}

// sourceRanges returns the Service's allowed client CIDRs as the canonical,
// sorted, comma-separated value for a rule's source_ip, or "" for
// unrestricted (no ranges set, or 0.0.0.0/0). spec.loadBalancerSourceRanges
// wins over the annotation. The LB must never end up open to everyone because
// a restriction could not be applied, so: a set-but-blank list, a malformed
// entry, or a list with only IPv6 entries is an error. IPv6 entries in a
// dual-stack list are dropped (logged) as long as an IPv4 entry remains.
func sourceRanges(svc *v1.Service) (string, error) {
	raw := svc.Spec.LoadBalancerSourceRanges
	if len(raw) == 0 {
		if ann := svc.Annotations[annotationSourceRanges]; ann != "" {
			raw = strings.Split(ann, ",")
		}
	}
	if len(raw) == 0 {
		return "", nil
	}
	seen := map[string]bool{}
	var out, v6 []string
	allowAll := false
	entries := 0
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		entries++
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return "", fmt.Errorf("service %s/%s: invalid loadBalancerSourceRanges entry %q (want a CIDR)", svc.Namespace, svc.Name, s)
		}
		if strings.Contains(s, ":") || n.IP.To4() == nil {
			v6 = append(v6, s)
			continue
		}
		if ones, _ := n.Mask.Size(); ones == 0 {
			allowAll = true // 0.0.0.0/0 allows everyone, same as no restriction
		}
		if c := n.String(); !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	if entries == 0 {
		return "", fmt.Errorf("service %s/%s: loadBalancerSourceRanges is set but contains no CIDRs", svc.Namespace, svc.Name)
	}
	if len(out) == 0 {
		return "", fmt.Errorf("service %s/%s: loadBalancerSourceRanges has only IPv6 entries %v; the VergeOS load balancer supports IPv4 ranges only", svc.Namespace, svc.Name, v6)
	}
	if len(v6) > 0 {
		klog.Warningf("Service %s/%s: ignoring IPv6 loadBalancerSourceRanges %v (IPv4 only)", svc.Namespace, svc.Name, v6)
	}
	if allowAll {
		return "", nil
	}
	sort.Strings(out)
	return strings.Join(out, ","), nil
}

// desiredRule is a rule a service port should have.
type desiredRule struct {
	name, description, protocol              string
	direction, action                        string
	destIP, destPorts, targetIP, targetPorts string
	sourceIP                                 string // comma-separated CIDRs; "" = any source
}

// ruleKey identifies a rule's role for one service port. A port has one
// incoming/translate rule (DNAT) and, when enabled, one outgoing/translate
// (SNAT) and one incoming/accept rule.
func ruleKey(direction, action, protocol, destPorts string) string {
	return direction + "/" + action + "/" + protocol + "/" + destPorts
}

func (d desiredRule) key() string { return ruleKey(d.direction, d.action, d.protocol, d.destPorts) }

const (
	suffixSNAT   = "-snat"
	suffixAccept = "-accept"
)

func buildDesired(owner lbOwner, svc *v1.Service, ip string, targets []string, sourceIP string, cfg lbConfig) ([]desiredRule, error) {
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
		port := strconv.Itoa(int(p.Port))
		nodePort := strconv.Itoa(int(p.NodePort))
		out = append(out, desiredRule{
			name:        owner.ruleName(vp, p.Port),
			description: owner.String(),
			protocol:    vp,
			direction:   "incoming",
			action:      "translate",
			destIP:      ip,
			destPorts:   port,
			targetIP:    targetIP,
			targetPorts: nodePort,
			sourceIP:    sourceIP,
		})
		if cfg.SourceNATTarget != "" {
			// Rewrites the source of client->node traffic so the node
			// replies to the LB vnet instead of straight to the client.
			out = append(out, desiredRule{
				name:        owner.ruleName(vp, p.Port) + suffixSNAT,
				description: owner.String(),
				protocol:    vp,
				direction:   "outgoing",
				action:      "translate",
				destIP:      targetIP,
				destPorts:   nodePort,
				targetIP:    cfg.SourceNATTarget,
			})
		}
		if cfg.AcceptRules {
			out = append(out, desiredRule{
				name:        owner.ruleName(vp, p.Port) + suffixAccept,
				description: owner.String(),
				protocol:    vp,
				direction:   "incoming",
				action:      "accept",
				destIP:      ip,
				destPorts:   port,
				sourceIP:    sourceIP,
			})
		}
	}
	return out, nil
}

func (d desiredRule) matches(r vergeos.VNetRule) bool {
	return r.Name == d.name && r.Description == d.description && r.Enabled &&
		r.Protocol == d.protocol && r.Direction == d.direction && r.Action == d.action &&
		r.DestinationIP == d.destIP && r.DestinationPorts == d.destPorts &&
		sameIPSet(r.TargetIP, d.targetIP) && r.TargetPorts == d.targetPorts &&
		sameIPSet(r.SourceIP, d.sourceIP)
}

// applyRules activates rule changes. VergeOS stages every rule edit on a vnet
// until someone applies them, and ApplyRules activates all of them, so the CCM
// only applies when it has something of its own to activate: it changed rules
// (changed) or an earlier apply of its own failed (in-memory flag). Changes it
// did not make (need_fw_apply with nothing of ours pending) are left staged and
// logged, unless config.ApplyStagedChanges opts in to applying them, which
// recovers from a CCM crash between writing rules and applying them.
// Note that an apply for our own change still activates whatever else is
// staged on the vnet; VergeOS has no per-rule apply.
func (l *loadBalancer) applyRules(ctx context.Context, changed bool) error {
	if !changed && !l.applyPending {
		net, err := l.network.Get(ctx, l.config.NetworkID)
		if err != nil {
			return fmt.Errorf("getting VNet %d: %w", l.config.NetworkID, err)
		}
		if net == nil || !net.NeedFWApply {
			return nil
		}
		if !l.config.ApplyStagedChanges {
			klog.Warningf("VNet %d has staged firewall changes (need_fw_apply) that this CCM did not make; not applying them (set loadBalancer.applyStagedChanges=true to apply, or apply them in VergeOS)", l.config.NetworkID)
			return nil
		}
		klog.Infof("VNet %d has staged firewall changes; applying (loadBalancer.applyStagedChanges=true)", l.config.NetworkID)
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

// releaseAddresses deletes every ipalias of ours on the vnet except keepIP
// (pass "" to keep none). It lists the vnet's addresses once and classifies
// each; foreign aliases are never touched and no per-IP lookups are made.
func (l *loadBalancer) releaseAddresses(ctx context.Context, owner lbOwner, svc *v1.Service, ownedIPs map[string]bool, keepIP string) error {
	if l.addrs == nil {
		return nil
	}
	list, err := l.addrs.ListByNetwork(ctx, l.config.NetworkID)
	if err != nil {
		return fmt.Errorf("listing VNet addresses: %w", err)
	}
	var errs []error
	for i := range list {
		a := &list[i]
		if a.IP == keepIP || classifyAddr(a, owner, a.IP, svc, ownedIPs) == addrForeign {
			continue
		}
		if err := l.addrs.Delete(ctx, int(a.Key)); err != nil && !vergeos.IsNotFoundError(err) {
			errs = append(errs, fmt.Errorf("deleting VNet address for %s: %w", a.IP, err))
			continue
		}
		klog.Infof("Deleted VNet address entry for %s", a.IP)
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
		k := ruleKey(r.Direction, r.Action, r.Protocol, r.DestinationPorts)
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
			Direction:        &d.direction,
			Action:           &d.action,
			DestinationIP:    &d.destIP,
			DestinationPorts: &d.destPorts,
			TargetIP:         &d.targetIP,
			TargetPorts:      &d.targetPorts,
			SourceIP:         &d.sourceIP,
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
			Direction:        &d.direction,
			Action:           &d.action,
			DestinationIP:    &d.destIP,
			DestinationPorts: &d.destPorts,
			TargetIP:         nilIfEmpty(d.targetIP),
			TargetPorts:      nilIfEmpty(d.targetPorts),
			SourceIP:         sourceIPPtr(d.sourceIP),
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
	ip := vipIP(owned)
	if ip == "" {
		return nil, false, nil
	}
	return lbStatus(ip), true, nil
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
	if err := validateService(svc); err != nil {
		return "", err
	}
	owner := newLBOwner(clusterName, svc)

	all, owned, err := l.listRules(ctx, owner, svc)
	if err != nil {
		return "", err
	}
	ip, err := l.chooseIP(ctx, svc, owner, all, owned)
	if err != nil {
		return "", err
	}
	sources, err := sourceRanges(svc)
	if err != nil {
		return "", err
	}
	desired, err := buildDesired(owner, svc, ip, sortedTargets(nodes), sources, l.config)
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
	moved := false
	for _, r := range owned {
		if r.Direction != "outgoing" && r.DestinationIP != ip {
			moved = true
		}
	}
	for _, s := range statusIPs(svc) {
		if s != ip {
			moved = true
		}
	}
	if moved {
		if err := l.releaseAddresses(ctx, owner, svc, ipsOf(owned), ip); err != nil {
			return "", err
		}
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
	for _, r := range owned {
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
	// An ipalias can be orphaned by a failed Ensure (alias is created before
	// the rules), leaving no rule or status IP to find it by. One list of the
	// vnet's addresses finds every alias we own; nothing is probed per IP.
	return l.releaseAddresses(ctx, owner, service, ipsOf(owned), "")
}
