package framework

import (
	"flag"
	"os"
	"path/filepath"

	"k8s.io/klog/v2"
)

var e2eContext = &E2EContext{}

type E2EContext struct {
	HubKubeConfig                      string
	SpokeKubeConfig                    string
	AgentKubeConfig                    string
	TestCluster                        string
	ExternalManagedKubeConfigNamespace string
	ExternalManagedKubeConfigSecret    string
	HostingClusterName                 string
	HostedInstallNamespace             string
}

func ParseFlags() {
	registerFlags()
	flag.Parse()
	defaultFlags()
	validateFlags()
}

func registerFlags() {
	flag.StringVar(&e2eContext.HubKubeConfig,
		"hub-kubeconfig",
		os.Getenv("KUBECONFIG"),
		"Path to kubeconfig of the hub cluster.")
	flag.StringVar(&e2eContext.SpokeKubeConfig,
		"spoke-kubeconfig",
		"",
		"Path to kubeconfig of the managed/spoke cluster. Defaults to --hub-kubeconfig.")
	flag.StringVar(&e2eContext.AgentKubeConfig,
		"agent-kubeconfig",
		"",
		"Path to kubeconfig of the cluster where the addon agent deployment runs. Defaults to --spoke-kubeconfig.")
	flag.StringVar(&e2eContext.TestCluster,
		"test-cluster",
		"",
		"The target cluster to run the e2e suite.")
	flag.StringVar(&e2eContext.ExternalManagedKubeConfigNamespace,
		"external-managed-kubeconfig-namespace",
		"",
		"Namespace of the hosted-mode external managed kubeconfig secret, propagated through AddOnDeploymentConfig when set.")
	flag.StringVar(&e2eContext.ExternalManagedKubeConfigSecret,
		"external-managed-kubeconfig-secret",
		"",
		"Name of the hosted-mode external managed kubeconfig secret, propagated through AddOnDeploymentConfig when set.")
	flag.StringVar(&e2eContext.HostingClusterName,
		"hosting-cluster-name",
		"",
		"Name of the registered hosting cluster. When set, the suite ensures and annotates the managed-serviceaccount ManagedClusterAddOn for hosted placement.")
	flag.StringVar(&e2eContext.HostedInstallNamespace,
		"hosted-install-namespace",
		"",
		"Dedicated addon install namespace on the hosting cluster, required when --hosting-cluster-name is set.")
}

func defaultFlags() {
	if len(e2eContext.HubKubeConfig) == 0 {
		home := os.Getenv("HOME")
		if len(home) > 0 {
			e2eContext.HubKubeConfig = filepath.Join(home, ".kube", "config")
		}
	}
	if len(e2eContext.SpokeKubeConfig) == 0 {
		e2eContext.SpokeKubeConfig = e2eContext.HubKubeConfig
	}
	if len(e2eContext.AgentKubeConfig) == 0 {
		e2eContext.AgentKubeConfig = e2eContext.SpokeKubeConfig
	}
}

func validateFlags() {
	if len(e2eContext.HubKubeConfig) == 0 {
		klog.Fatalf("--hub-kubeconfig is required")
	}
	if len(e2eContext.TestCluster) == 0 {
		klog.Fatalf("--test-cluster is required")
	}
	// A hosted-mode run needs distinct hub, spoke, and agent kubeconfigs.
	if len(e2eContext.HostingClusterName) != 0 {
		if len(e2eContext.HostedInstallNamespace) == 0 {
			klog.Fatalf("--hosted-install-namespace is required when --hosting-cluster-name is set")
		}
		if sameKubeConfigPath(e2eContext.HubKubeConfig, e2eContext.SpokeKubeConfig) {
			klog.Fatalf("--hub-kubeconfig and --spoke-kubeconfig must point to different kubeconfigs in hosted mode")
		}
		if sameKubeConfigPath(e2eContext.SpokeKubeConfig, e2eContext.AgentKubeConfig) {
			klog.Fatalf("--spoke-kubeconfig and --agent-kubeconfig must point to different kubeconfigs in hosted mode")
		}
	}
}

// sameKubeConfigPath reports whether two kubeconfig paths resolve to the same file.
func sameKubeConfigPath(a, b string) bool {
	return normalizeKubeConfigPath(a) == normalizeKubeConfigPath(b)
}

func normalizeKubeConfigPath(path string) string {
	if len(path) == 0 {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
