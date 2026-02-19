package k8s

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	gatewayclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
)

// Clients bundles the two API clients the controller needs.
type Clients struct {
	Core    kubernetes.Interface
	Gateway gatewayclient.Interface
}

// NewClients builds Kubernetes API clients, preferring in-cluster config and
// falling back to the local kubeconfig.
func NewClients() (*Clients, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("load kubernetes config: %w", err)
		}
	}

	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create core client: %w", err)
	}

	gw, err := gatewayclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create gateway client: %w", err)
	}

	return &Clients{Core: core, Gateway: gw}, nil
}
