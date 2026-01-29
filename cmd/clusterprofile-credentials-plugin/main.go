package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetes "k8s.io/client-go/kubernetes"
	clientauthenticationv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/manager/controller"
	"sigs.k8s.io/cluster-inventory-api/pkg/credentialplugin"
)

type Provider struct {
	// KubeClient is the typed client for core Kubernetes resources (e.g. Secret).
	KubeClient kubernetes.Interface
	// ManagedServiceAccount is the name of the managedserviceaccount
	ManagedServiceAccount string
}

// NewDefault constructs a Provider with managedserviceaccount name and pre-initialized typed clientsets
func NewDefault(msaName string) (*Provider, error) {
	if msaName == "" {
		return nil, fmt.Errorf("managed-serviceaccount name is required")
	}

	// Build Kubernetes rest.Config via in-cluster first, then fallback to kubeconfig
	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.Getenv("HOME") + "/.kube/config"
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("failed to build kube client config: %w", err)
		}
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return &Provider{
		KubeClient:            kubeClient,
		ManagedServiceAccount: msaName,
	}, nil
}

func (Provider) Name() string { return controller.ClusterProfileManagerName }

func (p Provider) GetToken(ctx context.Context, info clientauthenticationv1.ExecCredential) (clientauthenticationv1.ExecCredentialStatus, error) {
	// Require pre-initialized typed clients
	if p.KubeClient == nil {
		return clientauthenticationv1.ExecCredentialStatus{}, fmt.Errorf("provider clients are not initialized")
	}

	// Extract clusterName from ExecCredential extensions config
	type execClusterConfig struct {
		ClusterName string `json:"clusterName"`
	}

	// Validate presence of cluster config
	if info.Spec.Cluster == nil || len(info.Spec.Cluster.Config.Raw) == 0 {
		return clientauthenticationv1.ExecCredentialStatus{}, fmt.Errorf("missing ExecCredential.Spec.Cluster.Config")
	}

	var cfg execClusterConfig
	if err := json.Unmarshal(info.Spec.Cluster.Config.Raw, &cfg); err != nil {
		return clientauthenticationv1.ExecCredentialStatus{}, fmt.Errorf("invalid ExecCredential.Spec.Cluster.Config: %w", err)
	}
	if cfg.ClusterName == "" {
		return clientauthenticationv1.ExecCredentialStatus{}, fmt.Errorf("missing clusterName in ExecCredential.Spec.Cluster.Config")
	}

	// Retrieve the synced token secret from clusterprofile namespace
	// Secret naming format matches the controller's sync pattern: <clusterName>-<managedServiceAccountName>
	namespace := inferNamespace()
	tokenSecretName := fmt.Sprintf("%s-%s", cfg.ClusterName, p.ManagedServiceAccount)
	secret, err := p.KubeClient.CoreV1().Secrets(namespace).Get(ctx, tokenSecretName, metav1.GetOptions{})
	if err != nil {
		return clientauthenticationv1.ExecCredentialStatus{}, fmt.Errorf("failed to get synced credential secret %s/%s: %w", namespace, tokenSecretName, err)
	}

	// Extract the token from the secret data
	tokenData, ok := secret.Data[corev1.ServiceAccountTokenKey]
	if !ok || len(tokenData) == 0 {
		return clientauthenticationv1.ExecCredentialStatus{}, fmt.Errorf("secret %s/%s missing or empty %q key", namespace, tokenSecretName, corev1.ServiceAccountTokenKey)
	}

	return clientauthenticationv1.ExecCredentialStatus{Token: string(tokenData)}, nil
}

func inferNamespace() string {
	// First: Check NAMESPACE environment variable
	if n := os.Getenv("NAMESPACE"); strings.TrimSpace(n) != "" {
		return strings.TrimSpace(n)
	}

	// Second: Fallback to in-cluster namespace file
	const namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	if data, err := os.ReadFile(namespaceFile); err == nil && len(data) > 0 {
		return strings.TrimSpace(string(data))
	}

	// Third: Infer namespace from KUBECONFIG context using standard loading rules
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path := os.Getenv("KUBECONFIG"); strings.TrimSpace(path) != "" {
		rules.ExplicitPath = path
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	if n, _, err := cc.Namespace(); err == nil && strings.TrimSpace(n) != "" {
		return n
	}

	// Default namespace if all else fails
	return "default"
}

func main() {
	msaName := pflag.String("managed-serviceaccount", "", "Name of the ManagedServiceAccount to access spoke cluster (required)")
	pflag.Parse()

	if *msaName == "" {
		fmt.Fprintf(os.Stderr, "Error: --managed-serviceaccount flag is required\n")
		pflag.Usage()
		os.Exit(1)
	}

	p, err := NewDefault(*msaName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing provider: %v\n", err)
		os.Exit(1)
	}

	credentialplugin.Run(*p)
}
