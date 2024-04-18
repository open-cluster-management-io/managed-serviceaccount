package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	addonutils "open-cluster-management.io/addon-framework/pkg/utils"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/agent/controller"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/agent/health"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/commoncontroller"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	"open-cluster-management.io/managed-serviceaccount/pkg/features"
	"open-cluster-management.io/managed-serviceaccount/pkg/util"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(authv1beta1.AddToScheme(scheme))
}

func main() {

	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var clusterName string
	var spokeKubeconfig string
	var featureGatesFlags map[string]bool
	var leaseHealthCheck bool

	logger := klogr.New()
	klog.SetOutput(os.Stdout)
	klog.InitFlags(flag.CommandLine)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":38080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":38081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&clusterName, "cluster-name", "", "The name of the managed cluster.")
	flag.StringVar(&spokeKubeconfig, "spoke-kubeconfig", "", "The kubeconfig to talk to the managed cluster, "+
		"will use the in-cluster client if not specified.")
	flag.Var(
		cliflag.NewMapStringBool(&featureGatesFlags),
		"feature-gates",
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
			"Options are:\n"+strings.Join(features.FeatureGates.KnownFeatures(), "\n"))
	flag.BoolVar(&leaseHealthCheck, "lease-health-check", false, "Use lease to report health check.")
	flag.Parse()
	ctrl.SetLogger(logger)

	err := features.FeatureGates.SetFromMap(featureGatesFlags)
	if err != nil {
		klog.Fatalf("unable to set featuregates map: %v", err)
	}

	if len(clusterName) == 0 {
		klog.Fatal("missing --cluster-name")
	}

	var spokeCfg *rest.Config
	if len(spokeKubeconfig) > 0 {
		spokeCfg, err = clientcmd.BuildConfigFromFlags("", spokeKubeconfig)
		if err != nil {
			klog.Fatal("failed to build a spoke cluster client config from --spoke-kubeconfig")
		}
	} else {
		spokeCfg, err = rest.InClusterConfig()
		if err != nil {
			klog.Fatal("failed build a in-cluster spoke cluster client config")
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "managed-serviceaccount-addon-agent",
		LeaderElectionConfig:   spokeCfg,
		Cache: cache.Options{
			// Only watch resources in the managed cluster namespace on the hub cluster.
			DefaultNamespaces: map[string]cache.Config{
				clusterName: {},
			},
		},
	})
	if err != nil {
		klog.Fatal("unable to start manager")
	}

	hubNativeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		klog.Fatal("unable to instantiate a kubernetes native client")
	}

	spokeNativeClient, err := kubernetes.NewForConfig(spokeCfg)
	if err != nil {
		klog.Fatal("unable to build a spoke kubernetes client")
	}

	resources, err := spokeNativeClient.Discovery().ServerResourcesForGroupVersion("v1")
	if err != nil {
		klog.Fatalf("Failed api discovery in the spoke cluster: %v", err)
	}
	foundTokenRequest := false
	for _, r := range resources.APIResources {
		if r.Kind == "TokenRequest" {
			foundTokenRequest = true
		}
	}
	if !foundTokenRequest {
		klog.Warningf(`No "serviceaccounts/token" resource discovered in the managed cluster,` +
			`is --service-account-signing-key-file configured for the kube-apiserver?`)
	}

	spokeNamespace := os.Getenv("NAMESPACE")
	if len(spokeNamespace) == 0 {
		inClusterNamespace, err := util.GetInClusterNamespace()
		if err != nil {
			klog.Fatal("the agent should be either running in a container or specify NAMESPACE environment")
		}
		spokeNamespace = inClusterNamespace
	}

	spokeCache, err := cache.New(spokeCfg, cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&corev1.ServiceAccount{}: {
				Namespaces: map[string]cache.Config{
					spokeNamespace: {
						LabelSelector: labels.SelectorFromSet(
							labels.Set{
								common.LabelKeyIsManagedServiceAccount: "true",
							},
						),
					},
				},
			},
		},
	})
	if err != nil {
		klog.Fatal("unable to instantiate a spoke serviceaccount cache")
	}
	err = mgr.Add(spokeCache)
	if err != nil {
		klog.Fatal("unable to add spoke cache to manager")
	}

	if features.FeatureGates.Enabled(features.EphemeralIdentity) {
		if err := (commoncontroller.NewEphemeralIdentityReconciler(
			mgr.GetCache(),
			mgr.GetClient(),
		)).SetupWithManager(mgr); err != nil {
			klog.Error(err, "unable to register EphemeralIdentityReconciler")
			os.Exit(1)
		}
	}

	if err = (&controller.TokenReconciler{
		Cache:                      mgr.GetCache(),
		HubClient:                  mgr.GetClient(),
		HubNativeClient:            hubNativeClient,
		SpokeNamespace:             spokeNamespace,
		SpokeClientConfig:          spokeCfg,
		SpokeNativeClient:          spokeNativeClient,
		ClusterName:                clusterName,
		SpokeCache:                 spokeCache,
		CreateTokenByDefaultSecret: !foundTokenRequest,
	}).SetupWithManager(mgr); err != nil {
		klog.Fatalf("unable to create controller %v", "ManagedServiceAccount")
	}

	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancel()

	if leaseHealthCheck {
		leaseUpdater, err := health.NewAddonHealthUpdater(mgr.GetConfig(), clusterName, spokeCfg, spokeNamespace)
		if err != nil {
			klog.Fatalf("unable to create healthiness lease updater for controller %v", "ManagedServiceAccount")
		}
		go leaseUpdater.Start(ctx)
	}

	cc, err := addonutils.NewConfigChecker("managed-serviceaccount-agent", getHubKubeconfigPath())
	if err != nil {
		klog.Fatalf("unable to create config checker for controller %v", "ManagedServiceAccount")
	}
	go func() {
		if err = serveHealthProbes(":8000", cc.Check); err != nil {
			klog.Fatal(err)
		}
	}()

	if err := mgr.Start(ctx); err != nil {
		klog.Fatalf("unable to start controller manager: %v", err)
	}

}

// serveHealthProbes serves health probes and configchecker.
func serveHealthProbes(healthProbeBindAddress string, configCheck healthz.Checker) error {
	mux := http.NewServeMux()
	mux.Handle("/healthz", http.StripPrefix("/healthz", &healthz.Handler{Checks: map[string]healthz.Checker{
		"healthz-ping": healthz.Ping,
		"configz-ping": configCheck,
	}}))
	server := http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		Addr:              healthProbeBindAddress,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	klog.Infof("heath probes server is running...")
	return server.ListenAndServe()
}

func getHubKubeconfigPath() string {
	hubKubeconfigPath := os.Getenv("HUB_KUBECONFIG")
	if len(hubKubeconfigPath) == 0 {
		hubKubeconfigPath = "/etc/hub/kubeconfig"
	}
	return hubKubeconfigPath
}
