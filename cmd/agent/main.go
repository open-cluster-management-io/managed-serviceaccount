package main

import (
	"context"
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/agent/controller"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/agent/health"
	"open-cluster-management.io/managed-serviceaccount/pkg/util"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
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

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":38080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":38081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&clusterName, "cluster-name", "", "The name of the managed cluster.")
	flag.StringVar(&spokeKubeconfig, "spoke-kubeconfig", "", "The kubeconfig to talk to the managed cluster, "+
		"will use the in-cluster client if not specified.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if len(clusterName) == 0 {
		panic("missing --cluster-name")
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	var spokeCfg *rest.Config
	var err error
	if len(spokeKubeconfig) > 0 {
		spokeCfg, err = clientcmd.BuildConfigFromFlags("", spokeKubeconfig)
		if err != nil {
			setupLog.Error(err, "failed to build a spoke kubernetes client config from --spoke-kubeconfig")
			os.Exit(1)
		}
	} else {
		spokeCfg, err = rest.InClusterConfig()
		if err != nil {
			setupLog.Error(err, "failed to build a spoke kubernetes in-cluster client config")
			os.Exit(1)
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
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	hubNativeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to instantiate kubernetes native client")
		os.Exit(1)
	}

	spokeNativeClient, err := kubernetes.NewForConfig(spokeCfg)
	if err != nil {
		setupLog.Error(err, "failed to build a spoke kubernetes client")
		os.Exit(1)
	}

	spokeNamespace := os.Getenv("NAMESPACE")
	if len(spokeNamespace) == 0 {
		inClusterNamespace, err := util.GetInClusterNamespace()
		if err != nil {
			setupLog.Error(err, "the agent should be either running in a container or specify NAMESPACE environment")
			os.Exit(1)
		}
		spokeNamespace = inClusterNamespace
	}
	if err = (&controller.TokenReconciler{
		Cache:             mgr.GetCache(),
		HubClient:         mgr.GetClient(),
		HubNativeClient:   hubNativeClient,
		SpokeNamespace:    spokeNamespace,
		SpokeClientConfig: spokeCfg,
		SpokeNativeClient: spokeNativeClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ManagedServiceAccount")
		os.Exit(1)
	}

	ctx, _ := context.WithCancel(ctrl.SetupSignalHandler())

	leaseUpdater, err := health.NewAddonHealthUpdater(mgr.GetConfig(), clusterName)
	if err != nil {
		setupLog.Error(err, "unable to create healthiness lease updater", "controller", "ManagedServiceAccount")
		os.Exit(1)
	}
	leaseUpdater.Start(ctx)

	if err := mgr.Start(ctx); err != nil {
		panic(err)
	}
}
