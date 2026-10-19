package vergeos

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"
)

// AnnotationLoadBalancerNetworkID selects the vnet a Service's load balancer
// is created on. It must be the default loadBalancer.networkID or one of
// loadBalancer.additionalNetworks; the IP is allocated from that vnet's pool.
const AnnotationLoadBalancerNetworkID = "loadbalancer.vergeos.io/network-id"

// lbRouter serves each Service from the loadBalancer of the vnet it selects.
// Every vnet has its own loadBalancer, so IP allocation, ownership checks,
// locking and rule application stay scoped to one vnet.
type lbRouter struct {
	def     *loadBalancer
	workers []*loadBalancer // def first
}

// newLBRouter takes the default vnet's loadBalancer first.
func newLBRouter(workers ...*loadBalancer) *lbRouter {
	return &lbRouter{def: workers[0], workers: workers}
}

// networkIDs lists the selectable vnets, default first.
func (r *lbRouter) networkIDs() []int {
	ids := make([]int, len(r.workers))
	for i, w := range r.workers {
		ids[i] = w.config.NetworkID
	}
	return ids
}

// target returns the loadBalancer for svc: the annotated vnet, else the default.
func (r *lbRouter) target(svc *v1.Service) (*loadBalancer, error) {
	v, ok := svc.Annotations[AnnotationLoadBalancerNetworkID]
	if !ok {
		return r.def, nil
	}
	id, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("annotation %s=%q is not a vnet ID", AnnotationLoadBalancerNetworkID, v)
	}
	for _, w := range r.workers {
		if w.config.NetworkID == id {
			return w, nil
		}
	}
	return nil, fmt.Errorf("annotation %s=%d: vnet %d is not configured (loadBalancer.networkID or additionalNetworks; configured: %v)", AnnotationLoadBalancerNetworkID, id, id, r.networkIDs())
}

// others returns every worker except w.
func (r *lbRouter) others(w *loadBalancer) []*loadBalancer {
	var out []*loadBalancer
	for _, o := range r.workers {
		if o != w {
			out = append(out, o)
		}
	}
	return out
}

// releaseElsewhere removes svc's rules and ipaliases from every vnet except w.
// It runs after a successful Ensure/Update on w so a Service whose annotation
// changed does not leave a live LB on its old vnet. Only objects stamped with
// this Service's owner marker are touched. Errors are returned so the service
// controller retries.
func (r *lbRouter) releaseElsewhere(ctx context.Context, clusterName string, svc *v1.Service, w *loadBalancer) error {
	var errs []error
	for _, o := range r.others(w) {
		if err := o.EnsureLoadBalancerDeleted(ctx, clusterName, svc); err != nil {
			errs = append(errs, fmt.Errorf("releasing LoadBalancer %s/%s from vnet %d: %w", svc.Namespace, svc.Name, o.config.NetworkID, err))
		}
	}
	return errors.Join(errs...)
}

// GetLoadBalancer looks on the Service's selected vnet first, then the others
// (the annotation may have changed since the LB was created).
func (r *lbRouter) GetLoadBalancer(ctx context.Context, clusterName string, svc *v1.Service) (*v1.LoadBalancerStatus, bool, error) {
	order := r.workers
	if w, err := r.target(svc); err == nil {
		order = append([]*loadBalancer{w}, r.others(w)...)
	}
	for _, w := range order {
		st, exists, err := w.GetLoadBalancer(ctx, clusterName, svc)
		if err != nil || exists {
			return st, exists, err
		}
	}
	return nil, false, nil
}

func (r *lbRouter) GetLoadBalancerName(ctx context.Context, clusterName string, svc *v1.Service) string {
	return r.def.GetLoadBalancerName(ctx, clusterName, svc)
}

func (r *lbRouter) EnsureLoadBalancer(ctx context.Context, clusterName string, svc *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	w, err := r.target(svc)
	if err != nil {
		return nil, err
	}
	st, err := w.EnsureLoadBalancer(ctx, clusterName, svc, nodes)
	if err != nil {
		return nil, err
	}
	if err := r.releaseElsewhere(ctx, clusterName, svc, w); err != nil {
		return nil, err
	}
	return st, nil
}

func (r *lbRouter) UpdateLoadBalancer(ctx context.Context, clusterName string, svc *v1.Service, nodes []*v1.Node) error {
	if svc.DeletionTimestamp != nil {
		return nil
	}
	w, err := r.target(svc)
	if err != nil {
		return err
	}
	if err := w.UpdateLoadBalancer(ctx, clusterName, svc, nodes); err != nil {
		return err
	}
	return r.releaseElsewhere(ctx, clusterName, svc, w)
}

// EnsureLoadBalancerDeleted releases the Service from every vnet, so deletion
// works whatever the annotation says now (or if it became invalid).
func (r *lbRouter) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, svc *v1.Service) error {
	var errs []error
	for _, w := range r.workers {
		if err := w.EnsureLoadBalancerDeleted(ctx, clusterName, svc); err != nil {
			errs = append(errs, fmt.Errorf("vnet %d: %w", w.config.NetworkID, err))
		}
	}
	return errors.Join(errs...)
}
