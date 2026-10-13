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
| `image.repository` | `ghcr.io/verge-io/vergeos-cloud-controller-manager` | Container image |
| `image.tag` | `latest` | Image tag |
| `imagePullSecrets` | `[]` | Image pull secrets for private registries |
| `vergeos.host` | `""` | VergeOS API host (e.g., `10.8.8.26`) |
| `vergeos.apiKey` | `""` | API key authentication |
| `vergeos.verifySSL` | `"true"` | TLS certificate verification (set to `"false"` for self-signed certs) |
| `vergeos.existingSecret` | `""` | Use an existing Secret for credentials |
| `loadBalancer.enabled` | `true` | Enable LoadBalancer support |
| `loadBalancer.networkID` | `0` | VNet network ID |
| `loadBalancer.ipPool` | `[]` | Available LB IP addresses |
| `replicaCount` | `1` | Number of replicas |
| `nodeSelector` | `node-role.kubernetes.io/control-plane: "true"` | Schedule on control-plane nodes |
| `resources.requests.cpu` | `100m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `resources.limits.cpu` | `200m` | CPU limit |
| `resources.limits.memory` | `128Mi` | Memory limit |

### VergeOS Credentials

Credentials are provided via environment variables (injected from a Kubernetes Secret):

| Variable | Description |
|----------|-------------|
| `VERGEOS_HOST` | VergeOS API host (e.g., `10.8.8.26`) |
| `VERGEOS_API_KEY` | API key for authentication |
| `VERGEOS_VERIFY_SSL` | Verify TLS certificates (`true`/`false`, default: `true`). Set to `false` for self-signed certs. |

You can reference an existing secret instead of letting the chart create one:

```yaml
vergeos:
  existingSecret: my-vergeos-credentials
```

### Cloud Config

The CCM reads a JSON cloud config file at `/etc/kubernetes/vergeos-cloud-config.json` (managed by the Helm chart ConfigMap):

```json
{
  "loadBalancer": {
    "enabled": true,
    "networkID": 1,
    "ipPool": ["10.0.0.100", "10.0.0.101", "10.0.0.102"]
  }
}
```

## How It Works

### Node Lifecycle

The CCM implements the Kubernetes `InstancesV2` interface:

1. **InstanceMetadata** — Resolves a Kubernetes node to a VergeOS VM (by provider ID or name), then returns the provider ID (`vergeos://<vm-id>`), instance type (`<cpu>cpu-<ram>mb`), and internal IP addresses from VM NICs.
2. **InstanceExists** — Checks whether the backing VM still exists.
3. **InstanceShutdown** — Reports whether the VM is powered off.

### Load Balancer

The CCM implements the Kubernetes `LoadBalancer` interface using VergeOS VNet rules (an `ipalias` on the vnet plus one `incoming`/`translate` rule per Service port).

1. **EnsureLoadBalancer / UpdateLoadBalancer** — Converge: compute the desired rules (one per port: `<lb-ip>:<port>` → `<node-ips>:<nodePort>`), then create, update and delete the difference and call `ApplyRules` once. Added/removed ports and changed NodePorts or nodes all converge. Any failure is returned, so the service controller retries; the next call resumes from the partial state.
2. **EnsureLoadBalancerDeleted** — Deletes the Service's rules, applies, then releases its ipalias. Failures are returned and retried.

**Ownership.** Every rule and ipalias carries an exact marker in its `description`: `vergeos-ccm/v1 cluster=<c> namespace=<ns> name=<svc> uid=<uid>` (each value URL-escaped). Only objects whose description matches exactly are ever updated or deleted, so same-named Services in other namespaces, other clusters or recreated Services (new UID) never touch each other. Rule names are `k8s-<owner hash>-<tcp|udp>-<port>`.

**Requested IP.** Set `spec.loadBalancerIP` or the annotation `loadbalancer.vergeos.io/ip: <ip>` (if both are set they must match). The IP must be in `ipPool` and not used by another rule or vnet address, otherwise Ensure fails. Without a request the Service keeps the IP it already has, else gets the first free pool IP. Pin the IP for anything whose address is baked into other config (e.g. k0smotron API endpoints) — a recreated Service then gets the same IP back.

**Migrating from the old prefix scheme** (`k8s-<ns>-<svc>-<proto>-<port>`, empty description). A legacy rule is adopted (renamed and stamped in place, no traffic interruption) only if its name matches this exact namespace/service **and** its destination IP is already in the Service's `status.loadBalancer.ingress`. Legacy rules that cannot be proven this way are never modified or deleted; they must be removed by hand. Services without a status IP (or whose rules were created by a different Service with an ambiguous name) get a fresh LB on a free IP.

Not supported: `allocateLoadBalancerNodePorts: false`, SCTP, per-Service vnet/pool. Services with `spec.loadBalancerClass` are skipped by the cloud-provider service controller.

**Same-vnet reachability** (clients on the same vnet as the ipalias, e.g. hosted-CP workers) is not yet verified; the rules use `direction=incoming`, `action=translate`, `interface=auto`.

## Building

```bash
make build            # Local binary → bin/vergeos-cloud-controller-manager
make build-linux      # Cross-compile for Linux amd64
make docker-build     # Container image (linux/amd64, local)
make docker-push      # Build and push to ghcr.io/verge-io
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
