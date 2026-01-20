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

package manager

import (
	"context"
	"flag"
	"os"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/addonmanager"
	"open-cluster-management.io/addon-framework/pkg/utils"
	addonclient "open-cluster-management.io/api/client/addon/clientset/versioned"
	cpv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/commoncontroller"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/manager"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/manager/controller"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
	"open-cluster-management.io/managed-serviceaccount/pkg/features"
	"open-cluster-management.io/managed-serviceaccount/pkg/util"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(authv1beta1.AddToScheme(scheme))
	utilruntime.Must(cpv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func NewManager() *cobra.Command {
	managerOpts := NewHubManagerOptions()

	cmd := &cobra.Command{
		Use:   "manager",
		Short: "Start the managed service account addon manager",
		Run: func(cmd *cobra.Command, args []string) {
			if err := managerOpts.Run(); err != nil {
				klog.Fatal(err)
			}
		},
	}

	flags := cmd.Flags()
	managerOpts.AddFlags(flags)

	return cmd
}

func (o *HubManagerOptions) AddFlags(flags *pflag.FlagSet) {
	flags.StringVar(&o.MetricsAddr, "metrics-bind-address", ":38080", "The address the metric endpoint binds to.")
	flags.StringVar(&o.ProbeAddr, "health-probe-bind-address", ":38081", "The address the probe endpoint binds to.")
	flags.StringVar(&o.AddonAgentImageName, "agent-image-name", "quay.io/open-cluster-management/managed-serviceaccount:latest",
		"The image name of the addon agent")
	flags.BoolVar(&o.EnableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flags.Var(
		cliflag.NewMapStringBool(&o.FeatureGatesFlags),
		"feature-gates",
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
			"Options are:\n"+strings.Join(features.FeatureGates.KnownFeatures(), "\n"))
	flags.StringVar(&o.ImagePullSecretName, "agent-image-pull-secret", "",
		"The image pull secret that addon agent will use. "+
			"When specified, the content of image pull secret in the manager namespace on hub will be copied to the agent namespace on the managed cluster."+
			"This can also be configured with environment variable AGENT_IMAGE_PULL_SECRET.")
}

// HubManagerOptions holds configuration for hub manager controller
type HubManagerOptions struct {
	MetricsAddr          string
	EnableLeaderElection bool
	ProbeAddr            string
	AddonAgentImageName  string
	ImagePullSecretName  string
	FeatureGatesFlags    map[string]bool
}

// NewHubManagerOptions returns a HubManagerOptions
func NewHubManagerOptions() *HubManagerOptions {
	return &HubManagerOptions{}
}

func (o *HubManagerOptions) Run() error {
	logger := klog.Background()
	klog.SetOutput(os.Stdout)
	klog.InitFlags(flag.CommandLine)
	ctrl.SetLogger(logger)

	err := features.FeatureGates.SetFromMap(o.FeatureGatesFlags)
	if err != nil {
		setupLog.Error(err, "unable to set featuregates map")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: o.MetricsAddr},
		HealthProbeBindAddress: o.ProbeAddr,
		LeaderElection:         o.EnableLeaderElection,
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

	addonClient, err := addonclient.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to instantiating ocm addon client")
		os.Exit(1)
	}

	_, err = mgr.GetRESTMapper().ResourceFor(schema.GroupVersionResource{
		Group:    authv1beta1.GroupVersion.Group,
		Version:  authv1beta1.GroupVersion.Version,
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

	if len(o.ImagePullSecretName) == 0 {
		o.ImagePullSecretName = os.Getenv("AGENT_IMAGE_PULL_SECRET")
	}

	imagePullSecret := &corev1.Secret{}
	if len(o.ImagePullSecretName) != 0 {
		imagePullSecret, err = nativeClient.CoreV1().Secrets(hubNamespace).Get(
			context.TODO(),
			o.ImagePullSecretName,
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

	agentFactory := addonfactory.NewAgentAddonFactory(common.AddonName, manager.FS, "manifests/templates").
		WithConfigGVRs(utils.AddOnDeploymentConfigGVR).
		WithGetValuesFuncs(
			manager.GetDefaultValues(o.AddonAgentImageName, imagePullSecret),
			addonfactory.GetAgentImageValues(
				utils.NewAddOnDeploymentConfigGetter(addonClient),
				"Image",
				o.AddonAgentImageName,
			),
			addonfactory.GetAddOnDeploymentConfigValues(
				utils.NewAddOnDeploymentConfigGetter(addonClient),
				addonfactory.ToAddOnDeploymentConfigValues,
			),
		).
		WithAgentRegistrationOption(manager.NewRegistrationOption(nativeClient)).
		WithAgentDeployTriggerClusterFilter(utils.ClusterImageRegistriesAnnotationChanged)

	agentAddOn, err := agentFactory.BuildTemplateAgentAddon()
	if err != nil {
		setupLog.Error(err, "failed to build agent")
		os.Exit(1)
	}

	if err := addonManager.AddAgent(agentAddOn); err != nil {
		setupLog.Error(err, "unable to register addon agent")
		os.Exit(1)
	}

	if features.FeatureGates.Enabled(features.EphemeralIdentity) {
		if err := (commoncontroller.NewEphemeralIdentityReconciler(
			mgr.GetCache(),
			mgr.GetClient(),
		)).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to register EphemeralIdentityReconciler")
			os.Exit(1)
		}
	}

	if features.FeatureGates.Enabled(features.ClusterProfileCredSyncer) {
		if err := (controller.NewClusterProfileCredSyncer(
			mgr.GetCache(),
			mgr.GetClient(),
		)).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to register ClusterProfileCredSyncer")
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
	return nil
}
