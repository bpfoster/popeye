package k8s

//go:generate popeye gen

import (
	"fmt"

	"github.com/derailed/popeye/internal/config"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsapi "k8s.io/metrics/pkg/apis/metrics"
	mv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

var (
	supportedMetricsAPIVersions = []string{"v1beta1"}
	systemNS                    = []string{"kube-system", "kube-public"}
)

// Client represents a Kubernetes api server client.
type Client struct {
	*config.Config

	api kubernetes.Interface

	pos map[string]v1.Pod
	nss []v1.Namespace
	eps map[string]v1.Endpoints
}

// NewClient returns a dialable api server configuration.
func NewClient(config *config.Config) *Client {
	return &Client{Config: config}
}

// DialOrDie returns an api server client connection or dies.
func (c *Client) DialOrDie() kubernetes.Interface {
	client, err := c.Dial()
	if err != nil {
		panic(err)
	}
	return client
}

// Dial returns a handle to api server.
func (c *Client) Dial() (kubernetes.Interface, error) {
	if c.api != nil {
		return c.api, nil
	}

	cfg, err := c.Config.RESTConfig()
	if err != nil {
		return nil, err
	}

	if c.api, err = kubernetes.NewForConfig(cfg); err != nil {
		return nil, err
	}
	return c.api, nil
}

// ClusterHasMetrics checks if metrics server is available on the cluster.
func (c *Client) ClusterHasMetrics() bool {
	srv, err := c.Dial()
	if err != nil {
		return false
	}
	apiGroups, err := srv.Discovery().ServerGroups()
	if err != nil {
		return false
	}

	for _, discoveredAPIGroup := range apiGroups.Groups {
		if discoveredAPIGroup.Name != metricsapi.GroupName {
			continue
		}
		for _, version := range discoveredAPIGroup.Versions {
			for _, supportedVersion := range supportedMetricsAPIVersions {
				if version.Version == supportedVersion {
					return true
				}
			}
		}
	}
	return false
}

// FetchNodesMetrics fetch all node metrics.
func (c *Client) FetchNodesMetrics() ([]mv1beta1.NodeMetrics, error) {
	return FetchNodesMetrics(c)
}

// FetchPodsMetrics fetch all pods metrics in a given namespace.
func (c *Client) FetchPodsMetrics(ns string) ([]mv1beta1.PodMetrics, error) {
	return FetchPodsMetrics(c, ns)
}

// InUseNamespaces returns a list of namespaces referenced by pods.
func (c *Client) InUseNamespaces(nss []string) {
	pods, err := c.ListPods()
	if err != nil {
		return
	}

	ll := make(map[string]struct{})
	for _, p := range pods {
		ll[p.Namespace] = struct{}{}
	}

	var i int
	for k := range ll {
		nss[i] = k
		i++
	}
}

// ListEndpoints returns a endpoint by name.
func (c *Client) ListEndpoints() (map[string]v1.Endpoints, error) {
	if c.eps != nil {
		return c.eps, nil
	}

	ll, err := c.DialOrDie().CoreV1().Endpoints(c.Config.ActiveNamespace()).List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	c.eps = make(map[string]v1.Endpoints, len(ll.Items))
	for _, ep := range ll.Items {
		if !c.Config.ExcludedNS(ep.Namespace) {
			fqn := ep.Namespace + "/" + ep.Name
			c.eps[fqn] = ep
		}
	}

	return c.eps, nil
}

// GetEndpoints returns a endpoint by name.
func (c *Client) GetEndpoints(svcFQN string) (*v1.Endpoints, error) {
	eps, err := c.ListEndpoints()
	if err != nil {
		return nil, err
	}

	if ep, ok := eps[svcFQN]; ok {
		return &ep, nil
	}

	return nil, fmt.Errorf("Unable to find ep for service %s", svcFQN)
}

// ListServices lists all available services in a given namespace.
func (c *Client) ListServices() ([]v1.Service, error) {
	ll, err := c.DialOrDie().CoreV1().Services(c.Config.ActiveNamespace()).List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	svcs := make([]v1.Service, 0, len(ll.Items))
	for _, svc := range ll.Items {
		if !c.Config.ExcludedNS(svc.Namespace) {
			svcs = append(svcs, svc)
		}
	}

	return svcs, nil
}

// ListNodes list all available nodes on the cluster.
func (c *Client) ListNodes() ([]v1.Node, error) {
	ll, err := c.DialOrDie().CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	nodes := make([]v1.Node, 0, len(ll.Items))
	for _, no := range ll.Items {
		if !c.Config.ExcludedNode(no.Name) {
			nodes = append(nodes, no)
		}
	}

	return nodes, nil
}

// GetPod returns a pod via a label query.
func (c *Client) GetPod(sel map[string]string) (*v1.Pod, error) {
	pods, err := c.ListPods()
	if err != nil {
		return nil, err
	}

	for _, po := range pods {
		var found int
		for k, v := range sel {
			if pv, ok := po.Labels[k]; ok && pv == v {
				found++
			}
		}
		if found == len(sel) {
			return &po, nil
		}
	}

	return nil, fmt.Errorf("No pods match service selector")
}

// ListPods list all available pods.
func (c *Client) ListPods() (map[string]v1.Pod, error) {
	if len(c.pos) != 0 {
		return c.pos, nil
	}

	ll, err := c.DialOrDie().CoreV1().Pods(c.Config.ActiveNamespace()).List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	c.pos = make(map[string]v1.Pod, len(ll.Items))
	for _, po := range ll.Items {
		if !c.Config.ExcludedNS(po.Namespace) {
			fqn := po.Namespace + "/" + po.Name
			c.pos[fqn] = po
		}
	}

	return c.pos, nil
}

// ListNS lists all available namespaces.
func (c *Client) ListNS() ([]v1.Namespace, error) {
	if len(c.nss) != 0 {
		return c.nss, nil
	}

	var (
		nn  *v1.NamespaceList
		err error
	)
	if ns := c.Config.ActiveNamespace(); ns == "" {
		nn, err = c.DialOrDie().CoreV1().Namespaces().List(metav1.ListOptions{})
	} else {
		var n *v1.Namespace
		n, err = c.DialOrDie().CoreV1().Namespaces().Get(ns, metav1.GetOptions{})
		nn = &v1.NamespaceList{Items: []v1.Namespace{*n}}
	}

	if err != nil {
		return nil, err
	}

	c.nss = make([]v1.Namespace, 0, len(nn.Items))
	for _, ns := range nn.Items {
		if !c.Config.ExcludedNS(ns.Name) {
			c.nss = append(c.nss, ns)
		}
	}

	return c.nss, nil
}

func isSystemNS(ns string) bool {
	for _, n := range systemNS {
		if n == ns {
			return true
		}
	}
	return false
}
