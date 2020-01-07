/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package huaweicloud

import (
	"context"
	"fmt"
	"strings"

	lru "github.com/hashicorp/golang-lru"

	v1 "k8s.io/api/core/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog"
)

const (
	ELBIDAnnotation            = "kubernetes.io/elb.id"
	ELBClassAnnotation         = "kubernetes.io/elb.class"
	ELBMarkAnnotation          = "kubernetes.io/elb.mark"
	VPCIDAnnotation            = "kubernetes.io/elb.vpc.id"
	ELBSessionAffinityMode     = "kubernetes.io/session-affinity-mode"
	ELBSessionSourceIP         = "SOURCE_IP"
	Ping                       = "ping"
	Pong                       = "pong"
	HealthzCCE                 = "cce-healthz"
	ListenerDescription        = "Attention! It is auto-generated by CCE service, do not modify!"
	DefaultSessionAffinityTime = 1440
)

type LoadBalancerOpts struct {
	Apiserver string `json:"apiserver"`
	// SecretName is the name of 'Secret' object.
	SecretName   string       `json:"secretName"`
	SignerType   string       `json:"signerType"`
	ELBAlgorithm ELBAlgorithm `json:"elbAlgorithm"`
	TenantId     string       `json:"tenantId"`
	Region       string       `json:"region"`
	VPCId        string       `json:"vpcId"`
	SubnetId     string       `json:"subnetId"`
	ECSEndpoint  string       `json:"ecsEndpoint"`
	ELBEndpoint  string       `json:"elbEndpoint"`
	ALBEndpoint  string       `json:"albEndpoint"`
	NATEndpoint  string       `json:"natEndpoint"`
	VPCEndpoint  string       `json:"vpcEndpoint"`
}

// ELBAlgorithm
type ELBAlgorithm string

const (
	ELBAlgorithmRR  ELBAlgorithm = "roundrobin"
	ELBAlgorithmLC  ELBAlgorithm = "leastconn"
	ELBAlgorithmSRC ELBAlgorithm = "source"
)

type LoadBalanceVersion int

const (
	VersionNotNeedLB LoadBalanceVersion = iota //if the service type is not LoadBalancer
	VersionELB
	VersionALB
	VersionNAT
)

// NewLoadBalancer creates a load balancer handler.
func NewLoadBalancer(lrucache *lru.Cache, loadBalancerConf *LoadBalancerOpts, kubeClient corev1.CoreV1Interface, eventRecorder record.EventRecorder) *LoadBalancer {
	lb := LoadBalancer{}
	lb.providers = make(map[LoadBalanceVersion]cloudprovider.LoadBalancer, 3)

	lb.providers[VersionELB] = &ELBCloud{lrucache: lrucache, config: loadBalancerConf, kubeClient: kubeClient, eventRecorder: eventRecorder}
	lb.providers[VersionALB] = &ALBCloud{lrucache: lrucache, config: loadBalancerConf, kubeClient: kubeClient, eventRecorder: eventRecorder}
	lb.providers[VersionNAT] = &NATCloud{lrucache: lrucache, config: loadBalancerConf, kubeClient: kubeClient, eventRecorder: eventRecorder}

	return &lb
}

// LoadBalancer represents all kinds of load balancer.
type LoadBalancer struct {
	providers map[LoadBalanceVersion]cloudprovider.LoadBalancer
}

// Check if our LoadBalancer implements necessary interface
var _ cloudprovider.LoadBalancer = &LoadBalancer{}

// GetLoadBalancer returns whether the specified load balancer exists, and
// if so, what its status is.
// Implementations must treat the *v1.Service parameter as read-only and not modify it.
// Parameter 'clusterName' is the name of the cluster as presented to kube-controller-manager
func (lb *LoadBalancer) GetLoadBalancer(ctx context.Context, clusterName string, service *v1.Service) (status *v1.LoadBalancerStatus, exists bool, err error) {
	LBVersion, err := getLoadBalancerVersion(service)
	if err != nil {
		return nil, false, err
	}

	provider, exist := lb.providers[LBVersion]
	if !exist {
		return nil, false, nil
	}

	return provider.GetLoadBalancer(ctx, clusterName, service)
}

// GetLoadBalancerName returns the name of the load balancer. Implementations must treat the
// *v1.Service parameter as read-only and not modify it.
func (lb *LoadBalancer) GetLoadBalancerName(ctx context.Context, clusterName string, service *v1.Service) string {
	// TODO(RainbowMango): implement later
	return ""
}

// EnsureLoadBalancer creates a new load balancer 'name', or updates the existing one. Returns the status of the balancer
// Implementations must treat the *v1.Service and *v1.Node
// parameters as read-only and not modify them.
// Parameter 'clusterName' is the name of the cluster as presented to kube-controller-manager
func (lb *LoadBalancer) EnsureLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) (*v1.LoadBalancerStatus, error) {
	LBVersion, err := getLoadBalancerVersion(service)
	if err != nil {
		return nil, err
	}

	provider, exist := lb.providers[LBVersion]
	if !exist {
		return nil, nil
	}

	return provider.EnsureLoadBalancer(ctx, clusterName, service, nodes)
}

// UpdateLoadBalancer updates hosts under the specified load balancer.
// Implementations must treat the *v1.Service and *v1.Node
// parameters as read-only and not modify them.
// Parameter 'clusterName' is the name of the cluster as presented to kube-controller-manager
func (lb *LoadBalancer) UpdateLoadBalancer(ctx context.Context, clusterName string, service *v1.Service, nodes []*v1.Node) error {
	LBVersion, err := getLoadBalancerVersion(service)
	if err != nil {
		return err
	}

	provider, exist := lb.providers[LBVersion]
	if !exist {
		return nil
	}

	return provider.UpdateLoadBalancer(ctx, clusterName, service, nodes)
}

// EnsureLoadBalancerDeleted deletes the specified load balancer if it
// exists, returning nil if the load balancer specified either didn't exist or
// was successfully deleted.
// This construction is useful because many cloud providers' load balancers
// have multiple underlying components, meaning a Get could say that the LB
// doesn't exist even if some part of it is still laying around.
// Implementations must treat the *v1.Service parameter as read-only and not modify it.
// Parameter 'clusterName' is the name of the cluster as presented to kube-controller-manager
func (lb *LoadBalancer) EnsureLoadBalancerDeleted(ctx context.Context, clusterName string, service *v1.Service) error {
	LBVersion, err := getLoadBalancerVersion(service)
	if err != nil {
		return err
	}

	provider, exist := lb.providers[LBVersion]
	if !exist {
		return nil
	}

	return provider.EnsureLoadBalancerDeleted(ctx, clusterName, service)
}

func GetHealthCheckPort(service *v1.Service) *v1.ServicePort {
	for _, port := range service.Spec.Ports {
		if port.Name == HealthzCCE {
			return &port
		}
	}
	return nil
}

func GetListenerName(service *v1.Service) string {
	return string(service.UID)
}

// to suit for old version
// if the elb has been created with the old version
// its listener name is service.name+service.uid
func GetOldListenerName(service *v1.Service) string {
	return strings.Replace(service.Name+"_"+string(service.UID), ".", "_", -1)
}

func GetSessionAffinity(service *v1.Service) bool {
	if service.Annotations[ELBSessionAffinityMode] == ELBSessionSourceIP {
		return true
	}
	return false
}

// if the node not health, it will not be added to ELB
func CheckNodeHealth(node *v1.Node) (bool, error) {
	conditionMap := make(map[v1.NodeConditionType]*v1.NodeCondition)
	for i := range node.Status.Conditions {
		cond := node.Status.Conditions[i]
		conditionMap[cond.Type] = &cond
	}

	status := false
	if condition, ok := conditionMap[v1.NodeReady]; ok {
		if condition.Status == v1.ConditionTrue {
			status = true
		} else {
			status = false
		}
	}

	if node.Spec.Unschedulable {
		status = false
	}

	return status, nil
}

func getLoadBalancerVersion(service *v1.Service) (LoadBalanceVersion, error) {
	if service.Spec.Type != "LoadBalancer" {
		return VersionNotNeedLB, nil
	}

	class, ok := service.Annotations[ELBClassAnnotation]
	if !ok {
		klog.Warningf("Invalid load balancer version specified for service: %s", service.Name)
		return VersionNotNeedLB, fmt.Errorf("invalid load balancer version specified for service: %s", service.Name)
	}

	switch class {
	case "elasticity", "":
		klog.Infof("Load balancer Version I for service %v", service.Name)
		return VersionELB, nil
	case "union":
		klog.Infof("Load balancer Version II for service %v", service.Name)
		return VersionALB, nil
	case "dnat":
		klog.Infof("DNAT for service %v", service.Name)
		return VersionNAT, nil
	default:
		return 0, fmt.Errorf("Load balancer version unknown")
	}
}
