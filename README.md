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
| `image.repository` | `ghcr.io/encore-platform-engineering/vergeos-cloud-controller-manager` | Container image |
| `image.tag` | `latest` | Image tag |
| `imagePullSecrets` | `[]` | Image pull secrets for private registries |
| `vergeos.host` | `""` | VergeOS API host (e.g., `10.8.8.26`) |
| `vergeos.apiKey` | `""` | API key authentication |
| `vergeos.verifySSL` | `"true"` | TLS certificate verification (set to `"false"` for self-signed certs) |
| `vergeos.existingSecret` | `""` | Use an existing Secret for credentials |
| `nodeNetworkID` | `0` | Only use NIC DHCP leases on this vnet ID for node InternalIPs (0 = all NICs); guest-agent IPs are not filtered |
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
  "nodeNetworkID": 0,
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

1. **InstanceMetadata** — Resolves a Kubernetes node to a VergeOS VM, then returns the provider ID (`vergeos://<vm-uuid>`), instance type (`<cpu>cpu-<ram>mb`), and addresses: `InternalIP`s from qemu-guest-agent if present, otherwise from the VM's NIC DHCP leases (`machine_nics.ipaddress`, optionally limited to `nodeNetworkID`), plus a `Hostname` equal to the node name.
2. **InstanceExists** — Checks whether the backing VM still exists.
3. **InstanceShutdown** — Reports whether the VM is powered off.

**Node → VM resolution:** `vergeos://<vm-uuid>` (also the legacy `vergeos://<int $key>`) if the node has a providerID. Otherwise `status.nodeInfo.systemUUID` against the VM UUID, then the node name. Snapshots are never matched; more than one match is an error. Cluster API sets `vergeos://<vm-uuid>`, so the CCM's providerID agrees with the Machine.

### Load Balancer

The CCM implements the Kubernetes `LoadBalancer` interface using VergeOS VNet rules:

1. **EnsureLoadBalancer** — Allocates an IP from the pool, then creates a VNet NAT rule per service port: `k8s-<namespace>-<service>-<protocol>-<port>`. Rules translate `<allocated-ip>:<port>` to `<node-ips>:<nodePort>`. Calls `ApplyRules` to activate.
2. **UpdateLoadBalancer** — Updates the target IPs on existing rules when nodes change.
3. **EnsureLoadBalancerDeleted** — Deletes all VNet rules for the service and applies the change.

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
