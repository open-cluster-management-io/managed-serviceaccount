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
	"fmt"
	"os"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth" //nolint:revive // required for auth plugins
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
	cpv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/addonmanager"
	"open-cluster-management.io/addon-framework/pkg/agent"
	"open-cluster-management.io/addon-framework/pkg/utils"
	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
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

const (
	deployModeDeployment    = "Deployment"
	deployModeAddOnTemplate = "AddOnTemplate"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(addonv1beta1.Install(scheme))
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
	flags.StringVar(&o.DeployMode, "deploy-mode", deployModeDeployment,
		"Deployment mode for the manager. Valid values: 'Deployment' (default - runs addon manager and optional controllers), "+
			"'AddOnTemplate' (runs only ClusterProfileCredSyncer controller without addon manager).")
	flags.Var(
		cliflag.NewMapStringBool(&o.FeatureGatesFlags),
		"feature-gates",
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
			"Options are:\n"+strings.Join(features.FeatureGates.KnownFeatures(), "\n"))
	flags.StringVar(&o.ImagePullSecretName, "agent-image-pull-secret", "",
		"The image pull secret that addon agent will use. "+
			"When specified, the content of image pull secret in the manager namespace on hub will be copied to the agent namespace on the managed cluster."+
			"This can also be configured with environment variable AGENT_IMAGE_PULL_SECRET.")
	flags.BoolVar(&o.EnableNetworkPolicies, "enable-network-policies", false,
		"Enable NetworkPolicies for the managed-cluster managed-serviceaccount addon-agent")
}

// HubManagerOptions holds configuration for hub manager controller
type HubManagerOptions struct {
	MetricsAddr           string
	EnableLeaderElection  bool
	ProbeAddr             string
	AddonAgentImageName   string
	ImagePullSecretName   string
	DeployMode            string
	FeatureGatesFlags     map[string]bool
	EnableNetworkPolicies bool
}

// NewHubManagerOptions returns a HubManagerOptions
func NewHubManagerOptions() *HubManagerOptions {
	return &HubManagerOptions{}
}

func (o *HubManagerOptions) Run() error {
	logger := klog.Background()
	klog.SetOutput(os.Stdout)
	ctrl.SetLogger(logger)

	if err := o.validateDeployMode(); err != nil {
		return err
	}

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
		Cache:                  managedServiceAccountCacheOptions(o.DeployMode),
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
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

	if o.DeployMode != deployModeAddOnTemplate {
		addonManager, err := addonmanager.New(mgr.GetConfig())
		if err != nil {
			setupLog.Error(err, "unable to set up addon manager")
			os.Exit(1)
		}

		nativeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
		if err != nil {
			setupLog.Error(err, "unable to instantiate kubernetes native client")
			os.Exit(1)
		}

		hubNamespace := os.Getenv("NAMESPACE")
		if len(hubNamespace) == 0 {
			inClusterNamespace, err := util.GetInClusterNamespace()
			if err != nil {
				setupLog.Error(err, "the manager should be either running in a container or specify NAMESPACE environment")
				os.Exit(1)
			}
			hubNamespace = inClusterNamespace
		}

		if len(o.ImagePullSecretName) == 0 {
			o.ImagePullSecretName = os.Getenv("AGENT_IMAGE_PULL_SECRET")
		}

		imagePullSecret, err := getAgentImagePullSecret(context.TODO(), nativeClient, hubNamespace, o.ImagePullSecretName)
		if err != nil {
			setupLog.Error(err, "unable to get agent image pull secret")
			os.Exit(1)
		}

		if _, err := mgr.GetCache().GetInformer(context.Background(), &addonv1beta1.AddOnDeploymentConfig{}); err != nil {
			setupLog.Error(err, "unable to initialize addon deployment config cache")
			os.Exit(1)
		}
		deploymentConfigGetter := &cachedAddOnDeploymentConfigGetter{
			reader: mgr.GetCache(),
		}
		agentInstallNamespaceFunc, err := manager.SetupAgentInstallNamespaceResolver(
			context.Background(),
			mgr.GetCache(),
			utils.AgentInstallNamespaceFromDeploymentConfigFunc(deploymentConfigGetter),
		)
		if err != nil {
			setupLog.Error(err, "unable to index managed cluster addon placement")
			os.Exit(1)
		}

		agentFactory := addonfactory.NewAgentAddonFactory(common.AddonName, manager.FS, "manifests/charts/managed-serviceaccount-agent").
			WithScheme(manager.NewAgentScheme()).
			WithConfigGVRs(utils.AddOnDeploymentConfigGVR).
			WithConfigCheckEnabledOption().
			WithAgentHostedModeEnabledOption().
			// Use lease health for every manager-driven agent, including an addon
			// taken over from an AddOnTemplate installation.
			WithAgentHealthProber(&agent.HealthProber{Type: agent.HealthProberTypeLease}).
			WithAgentInstallNamespace(agentInstallNamespaceFunc).
			WithGetValuesFuncs(
				manager.GetDefaultValues(o.AddonAgentImageName, imagePullSecret, o.EnableNetworkPolicies),
				addonfactory.GetAgentImageValues(
					deploymentConfigGetter,
					"Image",
					o.AddonAgentImageName,
				),
				addonfactory.GetAddOnDeploymentConfigValues(
					deploymentConfigGetter,
					addonfactory.ToAddOnDeploymentConfigValues,
					manager.ToAddOnPrometheusValues,
					manager.ValidateAddOnAgentVariables,
				),
			).
			WithAgentRegistrationOption(manager.NewRegistrationOption(nativeClient)).
			WithAgentDeployTriggerClusterFilter(utils.ClusterImageRegistriesAnnotationChanged)

		agentAddOn, err := agentFactory.BuildHelmAgentAddon()
		if err != nil {
			setupLog.Error(err, "failed to build agent")
			os.Exit(1)
		}

		if err := addonManager.AddAgent(agentAddOn); err != nil {
			setupLog.Error(err, "unable to register addon agent")
			os.Exit(1)
		}

		if err := mgr.Add(addonManagerRunnable{start: addonManager.Start}); err != nil {
			setupLog.Error(err, "unable to register addon manager")
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
	}

	// Setup ClusterProfileCredSyncer controller if feature gate is enabled
	if features.FeatureGates.Enabled(features.ClusterProfile) {
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

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
	return nil
}

func managedServiceAccountCacheOptions(deployMode string) cache.Options {
	if deployMode == deployModeAddOnTemplate {
		return cache.Options{}
	}

	return cache.Options{ByObject: map[client.Object]cache.ByObject{
		&addonv1beta1.ManagedClusterAddOn{}: {
			Field: fields.OneTermEqualSelector("metadata.name", common.AddonName),
		},
	}}
}

// addonManagerRunnable preserves the addon manager's leader-elected placement
// while making the requirement independent of controller-runtime defaults.
type addonManagerRunnable struct {
	start func(context.Context) error
}

func (r addonManagerRunnable) Start(ctx context.Context) error {
	return r.start(ctx)
}

func (addonManagerRunnable) NeedLeaderElection() bool {
	return true
}

// cachedAddOnDeploymentConfigGetter replaces the addon client backed
// utils.NewAddOnDeploymentConfigGetter with a manager-cache read: the install
// namespace uniqueness check resolves every peer addon's
// deployment config per render, which must not become per-render hub API GETs.
type cachedAddOnDeploymentConfigGetter struct {
	reader client.Reader
}

func (g *cachedAddOnDeploymentConfigGetter) Get(
	ctx context.Context,
	namespace, name string,
) (*addonv1beta1.AddOnDeploymentConfig, error) {
	config := &addonv1beta1.AddOnDeploymentConfig{}
	if err := g.reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, config); err != nil {
		return nil, err
	}
	return config, nil
}

func (o *HubManagerOptions) validateDeployMode() error {
	switch o.DeployMode {
	case deployModeDeployment, deployModeAddOnTemplate:
		return nil
	default:
		return fmt.Errorf("unsupported --deploy-mode %q, must be %q or %q",
			o.DeployMode, deployModeDeployment, deployModeAddOnTemplate)
	}
}

func getAgentImagePullSecret(
	ctx context.Context,
	nativeClient kubernetes.Interface,
	hubNamespace, imagePullSecretName string,
) (*corev1.Secret, error) {
	if len(imagePullSecretName) == 0 {
		return nil, nil
	}

	imagePullSecret, err := nativeClient.CoreV1().Secrets(hubNamespace).Get(
		ctx,
		imagePullSecretName,
		metav1.GetOptions{},
	)
	if err != nil {
		return nil, errors.Wrap(err, "fail to get agent image pull secret")
	}
	if imagePullSecret.Type != corev1.SecretTypeDockerConfigJson {
		return nil, errors.Errorf("incorrect type for agent image pull secret")
	}

	return imagePullSecret, nil
}
