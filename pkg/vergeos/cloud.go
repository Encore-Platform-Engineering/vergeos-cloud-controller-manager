package vergeos

import (
	"fmt"
	"io"

	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

const ProviderName = "vergeos"

func init() {
	cloudprovider.RegisterCloudProvider(ProviderName, func(config io.Reader) (cloudprovider.Interface, error) {
		return newCloud(config)
	})
}

type cloud struct {
	config    *cloudConfig
	instances *instances
	lb        *loadBalancer
}

func newCloud(config io.Reader) (*cloud, error) {
	cfg, err := parseConfig(config)
	if err != nil {
		return nil, err
	}

	client, err := newVergeOSClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("vergeos cloud provider: %w", err)
	}

	c := &cloud{
		config:    cfg,
		instances: newInstances(client.VMs, client.MachineStatus, client.VMNICs, cfg.NodeNetworkID),
	}
	if cfg.LoadBalancer.Enabled {
		c.lb = newLoadBalancer(client.VNetRules, client.Networks, client.VNetAddresses, c.instances, cfg.LoadBalancer)
	}

	return c, nil
}

func (c *cloud) Initialize(clientBuilder cloudprovider.ControllerClientBuilder, stop <-chan struct{}) {
	klog.Infof("Initializing VergeOS cloud controller manager")
}

func (c *cloud) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	if c.lb == nil {
		return nil, false
	}
	return c.lb, true
}

func (c *cloud) Instances() (cloudprovider.Instances, bool) {
	return nil, false
}

func (c *cloud) InstancesV2() (cloudprovider.InstancesV2, bool) {
	return c.instances, true
}

func (c *cloud) Zones() (cloudprovider.Zones, bool) {
	return nil, false
}

func (c *cloud) Clusters() (cloudprovider.Clusters, bool) {
	return nil, false
}

func (c *cloud) Routes() (cloudprovider.Routes, bool) {
	return nil, false
}

func (c *cloud) ProviderName() string {
	return ProviderName
}

func (c *cloud) HasClusterID() bool {
	return false
}
