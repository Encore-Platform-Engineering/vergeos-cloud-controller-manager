# VergeOS Cloud Controller Manager

[![Go 1.25+](https://img.shields.io/badge/go-1.25+-00ADD8.svg?logo=go&logoColor=white)](https://go.dev/dl/)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-v1.32+-326CE5.svg?logo=kubernetes&logoColor=white)](https://kubernetes.io/)

A Kubernetes [Cloud Controller Manager](https://kubernetes.io/docs/concepts/architecture/cloud-controller/) (CCM) for [VergeOS](https://www.verge.io/). It integrates VergeOS virtual machines as Kubernetes nodes and provisions load balancers via VergeOS VNet rules.

## Features

- **Node Management** — Populates Kubernetes node metadata (provider ID, instance type, internal IPs) from VergeOS VMs. Detects VM existence and power state for node lifecycle management.
- **Load Balancing** — Provisions VergeOS VNet NAT/translate rules for `type: LoadBalancer` Services. Allocates IPs from a configurable pool and maps service ports to node ports automatically.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Kubernetes Cluster (VergeOS VMs)                        │
│                                                          │
│  ┌─────────────────────────────────────────────┐         │
│  │  vergeos-cloud-controller-manager           │         │
│  │  Deployment (kube-system)                   │         │
│  │                                             │         │
│  │  ├── cloud-node-controller                  │         │
│  │  │   (node metadata, provider ID, IPs)      │         │
│  │  ├── cloud-node-lifecycle-controller        │         │
│  │  │   (VM existence, power state)            │         │
│  │  └── service-lb-controller                  │         │
│  │      (VNet rules for LoadBalancer svcs)     │         │
│  └──────────────────┬──────────────────────────┘         │
│                     │                                    │
└─────────────────────┼────────────────────────────────────┘
                      │  VergeOS API
                      ▼
           ┌──────────────────────┐
           │  VergeOS             │
           │  VMs · VNets · NICs  │
           └──────────────────────┘
```

## Installation

### Prerequisites

1. **A Kubernetes cluster running on VergeOS VMs** — provisioned via Rancher, kubeadm, or similar.

2. **Download kubeconfig from Rancher** — Cluster Management > *your cluster* > ⋮ > Download KubeConfig. Save and set:
   ```bash
   export KUBECONFIG=~/Downloads/<cluster>-kubeconfig.yaml
   kubectl get nodes  # verify access
   ```

3. **Create image pull secret** (if using a private container registry like ghcr.io):
   ```bash
   kubectl -n kube-system create secret docker-registry ghcr-credentials \
     --docker-server=ghcr.io \
     --docker-username=<GITHUB_USER> \
     --docker-password=<GITHUB_PAT>
   ```

### Helm (recommended)

```bash
helm install vergeos-ccm ./charts/vergeos-cloud-controller-manager \
  --namespace kube-system \
  --set vergeos.host=<VERGEOS_HOST> \
  --set vergeos.apiKey=<API_KEY> \
  --set 'imagePullSecrets[0].name=ghcr-credentials'
```

With load balancing enabled (requires a VergeOS VNet network ID and IP pool):

```bash
helm install vergeos-ccm ./charts/vergeos-cloud-controller-manager \
  --namespace kube-system \
  --set vergeos.host=<VERGEOS_HOST> \
  --set vergeos.apiKey=<API_KEY> \
  --set 'imagePullSecrets[0].name=ghcr-credentials' \
  --set loadBalancer.enabled=true \
  --set loadBalancer.networkID=<VNET_ID> \
  --set 'loadBalancer.ipPool[0]=10.0.0.100' \
  --set 'loadBalancer.ipPool[1]=10.0.0.101'
```

### Install from OCI

Each `v*` tag publishes the chart (version = tag without `v`) to `oci://ghcr.io/encore-platform-engineering/charts`. The package is private, so log in first (a token with `read:packages`):

```bash
helm registry login ghcr.io -u <GITHUB_USER>
helm install vergeos-ccm oci://ghcr.io/encore-platform-engineering/charts/vergeos-cloud-controller-manager \
  --version 0.1.1 --namespace kube-system \
  --set vergeos.host=<VERGEOS_HOST> \
  --set vergeos.apiKey=<API_KEY> \
  --set 'imagePullSecrets[0].name=ghcr-credentials'
```

`helm template` takes the same arguments.

### Verify

```bash
kubectl -n kube-system get pods -l app=vergeos-ccm
kubectl -n kube-system logs -l app=vergeos-ccm --tail=30
kubectl get nodes -o wide  # Check for ProviderID and addresses
```

### Upgrade

```bash
helm --kubeconfig ~/Downloads/<cluster>.yaml upgrade vergeos-ccm ./charts/vergeos-cloud-controller-manager \
  --namespace kube-system \
  --set vergeos.host=<VERGEOS_HOST> \
  --set vergeos.apiKey=<API_KEY> \
  --set 'imagePullSecrets[0].name=ghcr-credentials'
```

### Uninstall

```bash
helm --kubeconfig ~/Downloads/<cluster>.yaml uninstall vergeos-ccm --namespace kube-system
kubectl -n kube-system delete secret ghcr-credentials  # if no longer needed
```

## Configuration

### Helm Values

| Value | Default | Description |
|-------|---------|-------------|
| `image.repository` | `ghcr.io/encore-platform-engineering/vergeos-cloud-controller-manager` | Container image |
| `image.tag` | `""` | Image tag; empty = `v` + chart `appVersion` (CI publishes on git tags `v*`) |
| `imagePullSecrets` | `[]` | Image pull secrets for private registries |
| `vergeos.host` | `""` | VergeOS API host or URL (e.g., `192.0.2.10` or `https://vergeos.example.net`); `https://` is assumed when no scheme is given |
| `vergeos.apiKey` | `""` | API key authentication |
| `vergeos.verifySSL` | `"true"` | TLS certificate verification. `"false"` disables it (the CCM logs a warning) — prefer `caBundle`/`caSecret` |
| `vergeos.caBundle` | `""` | Inline PEM CA to trust for the VergeOS API (private/self-signed CA) |
| `vergeos.caSecret` | `""` | Existing Secret (key `ca.crt`) with the CA to trust; alternative to `caBundle` |
| `external.enabled` | `false` | Run the CCM outside the workload cluster (see [Running outside the workload cluster](#running-outside-the-workload-cluster)). Forces `hostNetwork: false`, `dnsPolicy: ClusterFirst`, no tolerations, no mounted ServiceAccount token and renders no ServiceAccount/RBAC |
| `external.kubeconfigSecret` | `""` | Secret in the release namespace with the workload cluster kubeconfig (e.g. CAPI/k0smotron `<cluster>-kubeconfig`); required when `external.enabled` |
| `external.kubeconfigSecretKey` | `value` | Key of that Secret holding the kubeconfig (`value` is the CAPI/k0smotron key) |
| `extraArgs` | `[]` | Extra CCM command-line arguments, e.g. `["--cluster-name=tenant-a"]` |
| `extraEnv` / `extraVolumes` / `extraVolumeMounts` | `[]` | Extra container env, volumes and mounts |
| `vergeos.existingSecret` | `""` | Use an existing Secret for credentials |
| `nodeNetworkID` | `0` | Only use NIC DHCP leases on this vnet ID for node InternalIPs (0 = all NICs); guest-agent IPs are only used for NICs on that vnet (matched by MAC) |
| `loadBalancer.enabled` | `false` | Enable LoadBalancer support; requires `networkID` > 0 and a non-empty `ipPool` (chart fails to render otherwise) |
| `loadBalancer.networkID` | `0` | VNet network ID |
| `loadBalancer.ipPool` | `[]` | Available LB IP addresses |
| `loadBalancer.additionalNetworks` | `[]` | Extra `{networkID, ipPool}` vnets a Service can select with the `loadbalancer.vergeos.io/network-id` annotation (see "Per-Service vnet" below) |
| `replicaCount` | `1` | Number of replicas |
| `nodeSelector` | `{}` | No selector: runs on control-plane or worker-only clusters (e.g. k0smotron) |
| `tolerations` | control-plane, master, `node.cloudprovider.kubernetes.io/uninitialized`, not-ready, unreachable (all `operator: Exists`); not-ready/unreachable are tolerated for NoSchedule and, for NoExecute, only `tolerationSeconds: 300` so a dead node's CCM is rescheduled | Lets the CCM start on tainted/uninitialized/NotReady nodes |
| `affinity` | `{}` | Overrides the default preferred pod anti-affinity (used when `replicaCount` > 1) |
| `hostNetwork` | `true` | Host networking so the CCM can initialize nodes before the CNI is ready (needs a free host port `securePort`; not allowed by the `baseline`/`restricted` Pod Security levels, so keep it in `kube-system` or set `false` if your CNI is already up) |
| `dnsPolicy` | `Default` | Pod DNS policy. `Default` uses the node's resolv.conf because CoreDNS isn't running until nodes are initialized (worker-only clusters); set `ClusterFirstWithHostNet` to resolve in-cluster names |
| `securePort` | `10258` | Secure-serving port; `/healthz` is used for liveness/readiness probes |
| `priorityClassName` | `system-cluster-critical` | Pod priority class |
| `podSecurityContext` / `securityContext` | non-root `65532`, read-only rootfs, drop `ALL`, seccomp `RuntimeDefault` | Pod/container hardening |
| `resources.requests.cpu` | `100m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `resources.limits.cpu` | `200m` | CPU limit |
| `resources.limits.memory` | `128Mi` | Memory limit |

### RBAC

Least privilege: the CCM ServiceAccount has cluster-wide `get/list/watch` on nodes and services plus `tokenreviews`/`subjectaccessreviews` create. In `kube-system` only: leases (`get/create/update`), events, the `extension-apiserver-authentication` ConfigMap, ServiceAccount `create`, and `get` + `serviceaccounts/token create` limited to `node-controller`, `service-controller` and `route-controller`. Those three ServiceAccounts (`--use-service-account-credentials`) get their write permissions from Kubernetes' built-in `system:controller:*` roles.

### Running outside the workload cluster

By default the CCM runs inside the cluster it manages, so the VergeOS API key is a Secret in that cluster. On multi-tenant hosted control planes (e.g. k0smotron) tenant cluster-admins can read it, and the key can change anything VergeOS lets it (including other tenants' vnets). With `external.enabled` the CCM runs in the **management cluster** next to the hosted control plane and reaches the workload cluster through its kubeconfig, so the workload cluster never holds a VergeOS Secret:

```bash
helm upgrade --install vergeos-ccm ./charts/vergeos-cloud-controller-manager \
  --namespace tenant-a-system \
  --set vergeos.host=https://vergeos.example.com \
  --set vergeos.existingSecret=vergeos-credentials \
  --set external.enabled=true \
  --set external.kubeconfigSecret=tenant-a-kubeconfig \
  --set 'extraArgs={--cluster-name=tenant-a}'
```

Install it with the management cluster's kubeconfig, in the namespace holding the `<cluster>-kubeconfig` Secret (the Secret is mounted, so it must be in the release namespace). Notes:

- The kubeconfig is passed as `--kubeconfig`, `--authentication-kubeconfig` and `--authorization-kubeconfig`; node/service informers, leader election (a `Lease` in the workload cluster's `kube-system`) and the per-controller ServiceAccounts (`--use-service-account-credentials`) all use it. The chart renders no ServiceAccount, ClusterRole or Role in this mode; RBAC is whatever the kubeconfig's identity has in the workload cluster (the CAPI/k0smotron admin kubeconfig is sufficient; a restricted identity needs at least the permissions in [RBAC](#rbac) plus the rights to create the controller ServiceAccounts and their tokens).
- Mode defaults: `hostNetwork: false`, `dnsPolicy: ClusterFirst`, no tolerations, `automountServiceAccountToken: false`. `hostNetwork`, `dnsPolicy` and `tolerations` values are ignored. `nodeSelector`, `affinity`, `priorityClassName` and `resources` still apply. Nodes are initialized over the kubeconfig, so the CNI does not need to be up.
- The management cluster pod must reach both the workload API server address in the kubeconfig and the VergeOS API.
- Set a unique `--cluster-name` per workload cluster through `extraArgs`: it is part of the load balancer ownership, so clusters sharing one VNet must not use the same name (default `kubernetes`). Changing it later orphans existing rules.
- Install one release per workload cluster (each in its own namespace or with its own release name).
- The kubeconfig is read at startup; restart the Deployment after it rotates.

**VergeOS permissions the API key needs** (scope the key to what the CCM does; it only touches its configured VMs and vnet):

| Area | Operations |
|------|------------|
| VMs / machines | list and get VMs; list NICs (with DHCP leases) and read machine status (node metadata, provider ID, InternalIPs, power state) |
| VNet `loadBalancer.networkID` (only if LB enabled) | read the vnet; list, create, update, delete addresses (`ipalias`) and rules (NAT/translate) |

Whether VergeOS can restrict a key to a single vnet has not been verified; until it is, a leaked key is still broader than the CCM's own footprint, but it no longer sits in the tenant cluster.

### VergeOS Credentials

Credentials are provided via environment variables (injected from a Kubernetes Secret):

| Variable | Description |
|----------|-------------|
| `VERGEOS_HOST` | VergeOS API host or URL (e.g., `192.0.2.10` or `https://vergeos.example.net`); `https://` is assumed when no scheme is given |
| `VERGEOS_API_KEY` | API key for authentication |
| `VERGEOS_VERIFY_SSL` | Verify TLS certificates (`true`/`false`, default: `true`). `false` disables verification and logs a warning. |
| `VERGEOS_CA_FILE` | PEM file with extra CA certificates to trust (same as `caFile` in the cloud config). |

The CCM exits at startup with a clear error if `VERGEOS_HOST`, credentials, or the API itself are unreachable/invalid.

You can reference an existing secret instead of letting the chart create one:

```yaml
vergeos:
  existingSecret: my-vergeos-credentials
```

### Cloud Config

The CCM reads a JSON cloud config file at `/etc/kubernetes/vergeos-cloud-config.json` (managed by the Helm chart ConfigMap):

```json
{
  "nodeNetworkID": 0,
  "caFile": "/etc/vergeos/ca.crt",
  "loadBalancer": {
    "enabled": true,
    "networkID": 1,
    "ipPool": ["10.0.0.100", "10.0.0.101", "10.0.0.102"],
    "additionalNetworks": [
      {"networkID": 2, "ipPool": ["203.0.113.100", "203.0.113.101"]}
    ]
  }
}
```

`ipPool` entries (also in `additionalNetworks`) must be single IPv4 addresses (no CIDRs or ranges); duplicates and malformed entries make the CCM fail at startup. Each `additionalNetworks` entry needs a unique `networkID` > 0 (different from `loadBalancer.networkID`) and a non-empty `ipPool`.

## How It Works

### Node Lifecycle

The CCM implements the Kubernetes `InstancesV2` interface:

1. **InstanceMetadata** — Resolves a Kubernetes node to a VergeOS VM, then returns the provider ID (`vergeos://<vm-uuid>`), instance type (`<cpu>cpu-<ram>mb`), and addresses: `InternalIP`s from the VM's NIC DHCP leases (`machine_nics.ipaddress`, optionally limited to `nodeNetworkID`), falling back to qemu-guest-agent IPs of interfaces whose MAC matches a VM NIC on that vnet (docker/CNI/bridge/loopback/link-local are skipped), plus a `Hostname` equal to the node name. If no InternalIP is found it returns an error so the node stays uninitialized and is retried.
2. **InstanceExists** — Checks whether the backing VM still exists.
3. **InstanceShutdown** — Reports whether the VM is powered off.

**Node → VM resolution:** `vergeos://<vm-uuid>` (also the legacy `vergeos://<int $key>`) if the node has a providerID. Otherwise `status.nodeInfo.systemUUID` against the VM UUID, then the node name. Snapshots are never matched; more than one match is an error. Cluster API sets `vergeos://<vm-uuid>`, so the CCM's providerID agrees with the Machine.

### Load Balancer

The CCM implements the Kubernetes `LoadBalancer` interface using VergeOS VNet rules (an `ipalias` on the vnet plus one `incoming`/`translate` rule per Service port).

1. **EnsureLoadBalancer / UpdateLoadBalancer** — Converge: compute the desired rules (one per port: `<lb-ip>:<port>` → `<node-ips>:<nodePort>`), then create, update and delete the difference and call `ApplyRules` once. Added/removed ports and changed NodePorts or nodes all converge. Any failure is returned, so the service controller retries; the next call resumes from the partial state.
2. **EnsureLoadBalancerDeleted** — Deletes the Service's rules, applies, then releases its ipalias(es). Failures are returned and retried. Leftover aliases (e.g. from a failed Ensure) are found with a single list of the vnet's addresses filtered by the exact owner marker — the pool is never probed per IP.

**Ownership.** Every rule and ipalias carries an exact marker in its `description`: `vergeos-ccm/v1 cluster=<c> namespace=<ns> name=<svc> uid=<uid>` (each value URL-escaped). Only objects whose description matches exactly are ever updated or deleted, so same-named Services in other namespaces, other clusters or recreated Services (new UID) never touch each other. Rule names are `k8s-<owner hash>-<tcp|udp>-<port>`.

**Targets.** Each translate rule targets one address per node, taken from the node's VM NIC on `loadBalancer.networkID` (DHCP lease first, else the qemu-guest-agent IP of the interface with that NIC's MAC) — not from the node's `InternalIP`, which follows `nodeNetworkID`. So nodes may have their node IP on one vnet and a second NIC on the LB vnet. A node with no NIC address on the LB vnet (or whose VM no longer exists) is skipped and logged; if no node qualifies, Ensure/Update fail with a Service event. A failed API lookup also fails the sync rather than shrinking the target set. The CCM never falls back to an address on another vnet.

**Per-Service vnet.** By default every LB uses `loadBalancer.networkID` and `ipPool`. A Service can select another vnet with the annotation `loadbalancer.vergeos.io/network-id: "<id>"`; the ID must be the default vnet or one of `loadBalancer.additionalNetworks`, whose own `ipPool` is then used (an unknown or non-numeric value makes Ensure/Update fail with a Service event and creates nothing). Only configured vnets can be selected, so Service authors cannot place an LB on an arbitrary vnet. Everything is per vnet: IP allocation and the requested-IP check use that vnet's pool, ownership checks only look at that vnet, and targets are the nodes' NICs on that vnet (see Targets). If the annotation changes, the LB is created on the new vnet and then removed (rules and ipalias stamped with this Service's owner marker) from the others; because of that, each Ensure/Update also lists rules and addresses on every other configured vnet. Delete and GetLoadBalancer look at all configured vnets regardless of the current annotation. To keep a Service off the CCM entirely, set `spec.loadBalancerClass`.

**Requested IP.** Set `spec.loadBalancerIP` or the annotation `loadbalancer.vergeos.io/ip: <ip>` (if both are set they must match). The IP must be in `ipPool` and not used by another rule or vnet address, otherwise Ensure fails. Without a request the Service keeps the IP it already has, else gets the first free pool IP. Pin the IP for anything whose address is baked into other config (e.g. k0smotron API endpoints) — a recreated Service then gets the same IP back.

**Source ranges.** `spec.loadBalancerSourceRanges` (or the legacy annotation `service.beta.kubernetes.io/load-balancer-source-ranges`) is written to every rule's `source_ip` as sorted, comma-separated IPv4 CIDRs; changing or removing it updates the rules. The spec field wins over the annotation. A malformed entry, a list that is set but blank (e.g. annotation `,`), or a list with only IPv6 entries makes Ensure/Update fail with an error (the LB is never silently opened to everyone). In a dual-stack list IPv6 entries are dropped (logged) while at least one IPv4 entry remains. `0.0.0.0/0` means unrestricted. [INFERENCE: not verified on a live VergeOS that a `translate` rule with `source_ip` drops non-matching sources rather than letting them reach the router's own firewall.]

**`externalTrafficPolicy: Local` is rejected.** VergeOS translate rules have no health check, so traffic goes to every node's NodePort and `Local` would blackhole connections on nodes without a local endpoint. Ensure/Update fail with a clear error (visible as a Service event); use `Cluster` (the default). A Service flipped to `Local` errors on every sync (its existing rules are left as they were) until it is set back to `Cluster`. `healthCheckNodePort` is ignored.

**Migrating from the old prefix scheme** (`k8s-<ns>-<svc>-<proto>-<port>`, empty description). Old names are ambiguous (`a-b/c` and `a/b-c` produce the same names), so a name match proves nothing. A legacy rule is adopted (renamed and stamped in place, no traffic interruption) only if **all** hold: empty description; name equals the legacy name of one of the Service's current ports; destination IP is in the Service's `status.loadBalancer.ingress`; destination port equals that port's `port` and target port equals its `nodePort`. The legacy ipalias is stamped only if an adopted rule points at the same IP. Anything else (e.g. rules for ports since removed) is never modified or deleted and must be cleaned up by hand.

**Operational notes.**
- Run a single CCM replica (or `--leader-elect=true`). IP allocation is serialised in-process only; during a leader-election overlap two CCMs could pick the same free IP (VergeOS rejecting duplicate ipalias IPs is not verified).
- Changing `--cluster-name` changes the ownership marker: existing LBs look foreign, their IPs count as in use, and the Services get new LBs. Clean up the old rules/aliases by hand.
- ApplyRules is also issued when the vnet reports `need_fw_apply`, which recovers from a crash between writing rules and applying them (this also applies any other pending firewall change on that vnet).

Not supported: `allocateLoadBalancerNodePorts: false`, SCTP, IPv6 source ranges, `externalTrafficPolicy: Local`. Services with `spec.loadBalancerClass` are skipped by the cloud-provider service controller.

**Same-vnet reachability** (clients on the same vnet as the ipalias, e.g. hosted-CP workers) is not yet verified; the rules use `direction=incoming`, `action=translate`, `interface=auto`.

## Building

```bash
make build            # Local binary → bin/vergeos-cloud-controller-manager
make build-linux      # Cross-compile for Linux amd64
make docker-build     # Container image (linux/amd64, local)
make docker-push      # Build and push to ghcr.io/encore-platform-engineering
make test             # All tests with -race
make vet              # Static analysis
```

## Project Structure

```
cmd/vergeos-cloud-controller-manager/   Entry point
pkg/vergeos/
  ├── cloud.go           Cloud provider registration
  ├── client.go          API client interfaces
  ├── config.go          Cloud config parsing
  ├── instances.go       InstancesV2 implementation
  ├── loadbalancer.go    LoadBalancer implementation
  └── *_test.go          Tests
charts/                  Helm chart
```

## Dependencies

| Package | Purpose |
|---------|---------|
| [govergeos](https://github.com/verge-io/govergeos) | VergeOS Go SDK — all API interactions |
| [k8s.io/cloud-provider](https://pkg.go.dev/k8s.io/cloud-provider) | Kubernetes cloud provider framework |
| [k8s.io/klog/v2](https://pkg.go.dev/k8s.io/klog/v2) | Kubernetes-style structured logging |

## License

Apache License 2.0 — see [LICENSE](LICENSE) for details.
