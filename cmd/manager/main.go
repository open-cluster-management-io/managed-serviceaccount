/*
Copyright 2021.

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

package main

import (
	"context"
	"flag"
	"os"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/pkg/errors"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	"open-cluster-management.io/addon-framework/pkg/addonmanager"
	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/manager"
	"open-cluster-management.io/managed-serviceaccount/pkg/features"
	"open-cluster-management.io/managed-serviceaccount/pkg/util"
	ctrl "sigs.k8s.io/controller-runtime"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(authv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var addonAgentImageName string
	var agentInstallAll bool
	var imagePullSecretName string
	var featureGatesFlags map[string]bool

	logger := klogr.New()
	klog.SetOutput(os.Stdout)
	klog.InitFlags(flag.CommandLine)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":38080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":38081", "The address the probe endpoint binds to.")
	flag.StringVar(&addonAgentImageName, "agent-image-name", "quay.io/open-cluster-management/managed-serviceaccount:latest",
		"The image name of the addon agent")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(
		&agentInstallAll, "agent-install-all", false,
		"Configure the install strategy of agent on managed clusters. "+
			"Enabling this will automatically install agent on all managed cluster.")
	flag.Var(
		cliflag.NewMapStringBool(&featureGatesFlags),
		"feature-gates",
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
			"Options are:\n"+strings.Join(features.FeatureGates.KnownFeatures(), "\n"))
	flag.StringVar(&imagePullSecretName, "agent-image-pull-secret", "",
		"The image pull secret that addon agent will use. "+
			"When specified, the content of image pull secret in the manager namespace on hub will be copied to the agent namespace on the managed cluster."+
			"This can also be configured with environment variable AGENT_IMAGE_PULL_SECRET.")

	flag.Parse()

	ctrl.SetLogger(logger)
	err := features.FeatureGates.SetFromMap(featureGatesFlags)
	if err != nil {
		setupLog.Error(err, "unable to set featuregates map")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "managed-serviceaccount-addon-manager",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	addonManager, err := addonmanager.New(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	nativeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to instantiating kubernetes native client")
		os.Exit(1)
	}

	_, err = mgr.GetRESTMapper().ResourceFor(schema.GroupVersionResource{
		Group:    authv1alpha1.GroupVersion.Group,
		Version:  authv1alpha1.GroupVersion.Version,
		Resource: "managedserviceaccounts",
	})
	if err != nil {
		setupLog.Error(err, `no "managedserviceaccounts" resource found in the hub cluster, is the CRD installed?`)
		os.Exit(1)
	}

	hubNamespace := os.Getenv("NAMESPACE")
	if len(hubNamespace) == 0 {
		inClusterNamespace, err := util.GetInClusterNamespace()
		if err != nil {
			setupLog.Error(err, "the manager should be either running in a container or specify NAMESPACE environment")
		}
		hubNamespace = inClusterNamespace
	}

	if len(imagePullSecretName) == 0 {
		imagePullSecretName = os.Getenv("AGENT_IMAGE_PULL_SECRET")
	}

	imagePullSecret := &corev1.Secret{}
	if len(imagePullSecretName) != 0 {
		imagePullSecret, err = nativeClient.CoreV1().Secrets(hubNamespace).Get(
			context.TODO(),
			imagePullSecretName,
			metav1.GetOptions{},
		)
		if err != nil {
			setupLog.Error(err, "fail to get agent image pull secret")
			os.Exit(1)
		}
		if imagePullSecret.Type != corev1.SecretTypeDockerConfigJson {
			setupLog.Error(errors.Errorf("incorrect type for agent image pull secret"), "")
			os.Exit(1)
		}
	}

	if err := addonManager.AddAgent(
		manager.NewManagedServiceAccountAddonAgent(
			nativeClient,
			addonAgentImageName,
			agentInstallAll,
			imagePullSecret,
		),
	); err != nil {
		setupLog.Error(err, "unable to register addon agent")
		os.Exit(1)
	}

	if features.FeatureGates.Enabled(features.EphemeralIdentity) {
		if err := (&manager.EphemeralIdentityReconciler{
			Cache:     mgr.GetCache(),
			HubClient: mgr.GetClient(),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to register EphemeralIdentityReconciler")
			os.Exit(1)
		}
	}

	setupLog.Info("starting manager")

	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancel()

	if err := addonManager.Start(ctx); err != nil {
		setupLog.Error(err, "unable to start addon agent")
		os.Exit(1)
	}

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
