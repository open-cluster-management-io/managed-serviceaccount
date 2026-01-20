package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

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

// ProviderName is the name of the credential provider.
const ProviderName = controller.ClusterProfileNamespace

type Provider struct {
	// KubeClient is the typed client for core Kubernetes resources (e.g. Secret).
	KubeClient kubernetes.Interface
	// ManagedServiceAccountName is the name of the managedserviceaccount
	ManagedServiceAccountName string
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
		KubeClient:                kubeClient,
		ManagedServiceAccountName: msaName,
	}, nil
}

func (Provider) Name() string { return ProviderName }

func (p Provider) GetToken(ctx context.Context, info clientauthenticationv1.ExecCredential) (clientauthenticationv1.ExecCredentialStatus, error) {
	// Require pre-initialized typed clients
	if p.KubeClient == nil {
		return clientauthenticationv1.ExecCredentialStatus{}, fmt.Errorf("provider clients are not initialized; construct with NewDefault or set clients")
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
	// This corresponds to: ClusterProfile.name = clusterName, ManagedServiceAccount in namespace clusterName
	tokenSecretName := fmt.Sprintf("%s-%s", cfg.ClusterName, p.ManagedServiceAccountName)
	secret, err := p.KubeClient.CoreV1().Secrets(controller.ClusterProfileNamespace).Get(ctx, tokenSecretName, metav1.GetOptions{})
	if err != nil {
		return clientauthenticationv1.ExecCredentialStatus{}, fmt.Errorf("failed to get synced credential secret %s/%s: %w", controller.ClusterProfileNamespace, tokenSecretName, err)
	}

	// Extract the token from the secret data
	tokenData, ok := secret.Data[corev1.ServiceAccountTokenKey]
	if !ok || len(tokenData) == 0 {
		return clientauthenticationv1.ExecCredentialStatus{}, fmt.Errorf("secret %s/%s missing or empty %q key", controller.ClusterProfileNamespace, tokenSecretName, corev1.ServiceAccountTokenKey)
	}

	return clientauthenticationv1.ExecCredentialStatus{Token: string(tokenData)}, nil
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
