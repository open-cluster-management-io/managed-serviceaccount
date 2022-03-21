package main

import (
	"context"
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/agent/controller"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/agent/health"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	"open-cluster-management.io/managed-serviceaccount/pkg/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(authv1alpha1.AddToScheme(scheme))
}

func main() {

	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var clusterName string
	var spokeKubeconfig string

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

	flag.Parse()
	ctrl.SetLogger(logger)

	if len(clusterName) == 0 {
		klog.Fatal("missing --cluster-name")
	}

	var spokeCfg *rest.Config
	var err error
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
		Namespace:              clusterName,
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "managed-serviceaccount-addon-agent",
		LeaderElectionConfig:   spokeCfg,
		EventBroadcaster:       record.NewBroadcaster(),
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
	found := false
	for _, r := range resources.APIResources {
		if r.Kind == "TokenRequest" {
			found = true
		}
	}
	if !found {
		klog.Fatalf(`No "serviceaccounts/token" resource discovered in the managed cluster,` +
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
		SelectorsByObject: cache.SelectorsByObject{
			&corev1.ServiceAccount{}: {
				Label: labels.SelectorFromSet(
					labels.Set{
						common.LabelKeyIsManagedServiceAccount: "true",
					},
				),
			},
		},
		Namespace: spokeNamespace,
	})
	if err != nil {
		klog.Fatal("unable to instantiate a spoke serviceaccount cache")
	}
	err = mgr.Add(spokeCache)
	if err != nil {
		klog.Fatal("unable to add spoke cache to manager")
	}

	if err = (&controller.TokenReconciler{
		Cache:             mgr.GetCache(),
		HubClient:         mgr.GetClient(),
		HubNativeClient:   hubNativeClient,
		SpokeNamespace:    spokeNamespace,
		SpokeClientConfig: spokeCfg,
		SpokeNativeClient: spokeNativeClient,
		ClusterName:       clusterName,
		SpokeCache:        spokeCache,
	}).SetupWithManager(mgr); err != nil {
		klog.Fatalf("unable to create controller %v", "ManagedServiceAccount")
	}

	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancel()

	leaseUpdater, err := health.NewAddonHealthUpdater(mgr.GetConfig(), clusterName, spokeCfg, spokeNamespace)
	if err != nil {
		klog.Fatalf("unable to create healthiness lease updater for controller %v", "ManagedServiceAccount")
	}
	go leaseUpdater.Start(ctx)

	if err := mgr.Start(ctx); err != nil {
		klog.Fatalf("unable to start controller manager: %v", err)
	}

}
