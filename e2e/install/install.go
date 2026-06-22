package install

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive,staticcheck // idiomatic ginkgo usage
	. "github.com/onsi/gomega"    //nolint:revive,staticcheck // idiomatic gomega usage

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/provisioner"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const (
	installTestBasename = "install"
	installWaitTimeout  = 2 * time.Minute
	// The first hosted rollout serializes addon registration, provisioner
	// error backoff, and kubelet volume-mount retries, and it runs in a
	// BeforeSuite where a timeout fails every spec; give it a larger budget.
	hostedRolloutTimeout          = 5 * time.Minute
	installPollInterval           = 2 * time.Second
	agentDeploymentName           = "managed-serviceaccount-addon-agent"
	provisionerDeploymentName     = "managed-serviceaccount-kubeconfig-provisioner"
	managedKubeConfigSecretSuffix = "-managed-kubeconfig"
	addonDeploymentConfigGroup    = "addon.open-cluster-management.io"
	addonDeploymentConfigResource = "addondeploymentconfigs"
	defaultAgentImage             = "quay.io/open-cluster-management/managed-serviceaccount:latest"
	hostedAgentDeployConfigName   = "hosted-agent-config"
	hostedProvisionerSyncInterval = "5s"
	hostedTokenPropagationWindow  = 90 * time.Second
	// The addon-framework lease updater waits at most 75 seconds between
	// reconciles. Two full intervals prove the invalid managed credential gates
	// lease creation instead of merely delaying it.
	hostedLeaseAbsenceWindow = 150 * time.Second
)

// Seed the hosted agent prerequisites in a BeforeSuite so they run before any
// spec regardless of Ginkgo randomization.
var _ = BeforeSuite(func() {
	f := framework.NewSuiteFramework(installTestBasename)
	if !f.IsHostedMode() {
		return
	}
	hostedRolloutStartedAt := time.Now()

	By("Seed external managed kubeconfig secret for hosted agent")
	seedExternalManagedKubeConfigSecret(f)

	By("Ensure ManagedClusterAddOn places the agent on the hosting cluster")
	ensureHostedManagedClusterAddOn(f)

	By("Apply AddOnDeploymentConfig for the agent on the hosting cluster")
	ensureHostedAddOnDeploymentConfig(f)

	By("Wait for hosted addon rollout")
	waitForHostedAddonRollout(f, hostedRolloutStartedAt)
})

var _ = Describe("Addon Installation Test", Label("install"),
	func() {
		f := framework.NewE2EFramework(installTestBasename)
		It("Addon healthiness should work", func() {
			waitManagedClusterAddonAvailable(f)
		})

		It("Hosted agent reloads its managed cluster token without restarting", func() {
			if !f.IsHostedMode() {
				Skip("tokenFile rotation is specific to hosted agent placement")
			}
			verifyHostedManagedTokenRotation(f)
		})

		It("Hosted addon health follows managed cluster reachability", func() {
			if !f.IsHostedMode() {
				Skip("managed cluster health gating is specific to hosted agent placement")
			}
			verifyHostedManagedClusterHealth(f)
		})

		It("Addon can be configured with AddOnDeploymentConfig", func() {
			deployConfigName := "tolerations-deploy-config"
			nodeSelector := map[string]string{"kubernetes.io/os": "linux"}
			tolerations := []corev1.Toleration{{Key: "node-role.kubernetes.io/infra", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}

			waitManagedClusterAddonAvailable(f)
			c := f.HubRuntimeClient()
			addon, err := getManagedClusterAddon(c, f.TestClusterName())
			Expect(err).NotTo(HaveOccurred())
			originalConfigs := slices.Clone(addon.Spec.Configs)
			addonInstallNamespace := addon.Status.Namespace
			agentDeploy, err := getAgentDeployment(f, addonInstallNamespace)
			Expect(err).NotTo(HaveOccurred())
			originalNodeSelector := maps.Clone(agentDeploy.Spec.Template.Spec.NodeSelector)
			originalTolerations := slices.Clone(agentDeploy.Spec.Template.Spec.Tolerations)

			DeferCleanup(func() {
				By("Restore managed-serviceaccount addon deployment config")
				Eventually(func() error {
					return setManagedClusterAddonConfigs(c, f.TestClusterName(), originalConfigs)
				}).WithTimeout(installWaitTimeout).Should(Succeed())
				Eventually(func() error {
					return deleteAddOnDeploymentConfig(c, f.TestClusterName(), deployConfigName)
				}).WithTimeout(installWaitTimeout).Should(Succeed())
				waitManagedClusterAddonAvailable(f)
				waitAgentDeploymentRolledOut(f, addonInstallNamespace, func(deploy *appsv1.Deployment) error {
					return expectAgentPlacement(deploy, originalNodeSelector, originalTolerations)
				})
			})

			deployConfig := &addonv1beta1.AddOnDeploymentConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deployConfigName,
					Namespace: f.TestClusterName(),
				},
			}
			By("Prepare a AddOnDeploymentConfig for managed-serviceaccount addon")
			Eventually(func() error {
				_, err := controllerutil.CreateOrUpdate(context.TODO(), c, deployConfig, func() error {
					deployConfig.Spec = addonv1beta1.AddOnDeploymentConfigSpec{
						NodePlacement: &addonv1beta1.NodePlacement{
							NodeSelector: nodeSelector,
							Tolerations:  tolerations,
						},
					}
					if f.IsHostedMode() {
						deployConfig.Spec.AgentInstallNamespace = f.HostedInstallNamespace()
					}
					appendManagedKubeConfigVariables(f, &deployConfig.Spec)
					return nil
				})
				return err
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Add the config to managed-serviceaccount addon")
			Eventually(func() error {
				return setManagedClusterAddonConfigs(
					c,
					f.TestClusterName(),
					[]addonv1beta1.AddOnConfig{addonDeploymentConfigReference(f.TestClusterName(), deployConfigName)},
				)
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Ensure the config is referenced")
			Eventually(func() error {
				addon, err := getManagedClusterAddon(c, f.TestClusterName())
				if err != nil {
					return err
				}

				for _, ref := range addon.Status.ConfigReferences {
					if ref.Group == addonDeploymentConfigGroup && ref.Resource == addonDeploymentConfigResource {
						return nil
					}
				}

				return fmt.Errorf("expected config reference %s/%s not found in %v",
					f.TestClusterName(), deployConfigName, addon.Status.ConfigReferences)
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Ensure the managed serviceaccount addon agent is configured")
			waitAgentDeploymentRolledOutInAddonNamespace(f, func(deploy *appsv1.Deployment) error {
				return expectAgentPlacement(deploy, nodeSelector, tolerations)
			})

			By("Ensure the managed-serviceaccount is available")
			waitManagedClusterAddonAvailable(f)
		})

		It("Addon install namespace can be configured with AddOnDeploymentConfig", Label("deployment-install"), func() {
			deployConfigName := "install-namespace-deploy-config"
			agentInstallNamespace := "managed-serviceaccount-config-test"
			nodeSelector := map[string]string{"kubernetes.io/os": "linux"}
			tolerations := []corev1.Toleration{{Key: "node-role.kubernetes.io/infra", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}

			waitManagedClusterAddonAvailable(f)
			c := f.HubRuntimeClient()
			addon, err := getManagedClusterAddon(c, f.TestClusterName())
			Expect(err).NotTo(HaveOccurred())
			originalConfigs := slices.Clone(addon.Spec.Configs)
			addonInstallNamespace := addon.Status.Namespace
			agentDeploy, err := getAgentDeployment(f, addonInstallNamespace)
			Expect(err).NotTo(HaveOccurred())
			originalNodeSelector := maps.Clone(agentDeploy.Spec.Template.Spec.NodeSelector)
			originalTolerations := slices.Clone(agentDeploy.Spec.Template.Spec.Tolerations)

			DeferCleanup(func() {
				By("Restore managed-serviceaccount addon deployment config")
				Eventually(func() error {
					return setManagedClusterAddonConfigs(c, f.TestClusterName(), originalConfigs)
				}).WithTimeout(installWaitTimeout).Should(Succeed())
				Eventually(func() error {
					return deleteAddOnDeploymentConfig(c, f.TestClusterName(), deployConfigName)
				}).WithTimeout(installWaitTimeout).Should(Succeed())
				waitManagedClusterAddonAvailable(f)
				waitAgentDeploymentRolledOut(f, addonInstallNamespace, func(deploy *appsv1.Deployment) error {
					return expectAgentPlacement(deploy, originalNodeSelector, originalTolerations)
				})
			})

			deployConfig := &addonv1beta1.AddOnDeploymentConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deployConfigName,
					Namespace: f.TestClusterName(),
				},
			}
			By("Prepare a AddOnDeploymentConfig for managed-serviceaccount addon")
			Eventually(func() error {
				_, err := controllerutil.CreateOrUpdate(context.TODO(), c, deployConfig, func() error {
					deployConfig.Spec = addonv1beta1.AddOnDeploymentConfigSpec{
						AgentInstallNamespace: agentInstallNamespace,
						NodePlacement: &addonv1beta1.NodePlacement{
							NodeSelector: nodeSelector,
							Tolerations:  tolerations,
						},
					}
					appendManagedKubeConfigVariables(f, &deployConfig.Spec)
					return nil
				})
				return err
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Add the config to managed-serviceaccount addon")
			Eventually(func() error {
				return setManagedClusterAddonConfigs(
					c,
					f.TestClusterName(),
					[]addonv1beta1.AddOnConfig{addonDeploymentConfigReference(f.TestClusterName(), deployConfigName)},
				)
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Ensure the config is referenced")
			Eventually(func() error {
				addon, err := getManagedClusterAddon(c, f.TestClusterName())
				if err != nil {
					return err
				}
				if len(addon.Status.ConfigReferences) == 0 {
					return fmt.Errorf("no config references in addon status")
				}

				found := false
				for _, ref := range addon.Status.ConfigReferences {
					if ref.Resource != addonDeploymentConfigResource || ref.Group != addonDeploymentConfigGroup {
						continue
					}
					if ref.DesiredConfig == nil ||
						ref.DesiredConfig.Name != deployConfigName ||
						ref.DesiredConfig.Namespace != f.TestClusterName() {
						return fmt.Errorf("unexpected config references %v", addon.Status.ConfigReferences)
					}
					if ref.DesiredConfig.SpecHash == "" {
						return fmt.Errorf("desired config spec hash is empty in config references %v", addon.Status.ConfigReferences)
					}
					if ref.LastObservedGeneration != deployConfig.Generation {
						return fmt.Errorf("last observed generation = %d, expected %d (config references %v)",
							ref.LastObservedGeneration, deployConfig.Generation, addon.Status.ConfigReferences)
					}
					found = true
				}
				if !found {
					return fmt.Errorf("no matching config reference for %s/%s in %v",
						f.TestClusterName(), deployConfigName, addon.Status.ConfigReferences)
				}
				if !meta.IsStatusConditionTrue(addon.Status.Conditions, addonv1beta1.ManagedClusterAddOnConditionConfigured) {
					return fmt.Errorf("addon is not configured: %v", addon.Status.Conditions)
				}
				if addon.Status.Namespace != agentInstallNamespace {
					return fmt.Errorf("addon is installed in %q, want %q", addon.Status.Namespace, agentInstallNamespace)
				}
				return nil
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Ensure the managed serviceaccount addon agent is configured")
			waitAgentDeploymentRolledOut(f, agentInstallNamespace, func(deploy *appsv1.Deployment) error {
				return expectAgentPlacement(deploy, nodeSelector, tolerations)
			})

			By("Ensure the managed-serviceaccount is available")
			waitManagedClusterAddonAvailable(f)
		})

		It("Agent image should be overridden by cluster annotation", func() {
			waitManagedClusterAddonAvailable(f)

			By("Get Addon agent install namespace")
			addon, err := getManagedClusterAddon(f.HubRuntimeClient(), f.TestClusterName())
			Expect(err).NotTo(HaveOccurred())
			addonInstallNamespace := addon.Status.Namespace

			cluster := &clusterv1.ManagedCluster{}
			err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{Name: f.TestClusterName()}, cluster)
			Expect(err).NotTo(HaveOccurred())
			originalImageRegistriesAnnotation, hadOriginalImageRegistriesAnnotation :=
				cluster.Annotations[clusterv1.ClusterImageRegistriesAnnotationKey]

			DeferCleanup(func() {
				By("Restore the managed cluster annotation")
				Eventually(func() error {
					return restoreManagedClusterImageRegistriesAnnotation(
						f.HubRuntimeClient(),
						f.TestClusterName(),
						originalImageRegistriesAnnotation,
						hadOriginalImageRegistriesAnnotation,
					)
				}).WithTimeout(installWaitTimeout).Should(Succeed())
				waitAgentDeploymentRolledOut(f, addonInstallNamespace, func(deploy *appsv1.Deployment) error {
					return expectAgentImage(deploy, defaultAgentImage)
				})
				waitManagedClusterAddonAvailable(f)
			})

			By("Prepare cluster annotation for addon image override config")
			overrideRegistries := addonv1beta1.AddOnDeploymentConfigSpec{
				Registries: []addonv1beta1.ImageMirror{
					{
						Source: "quay.io/open-cluster-management",
						Mirror: "quay.io/ocm",
					},
				},
			}
			registriesJSON, err := json.Marshal(overrideRegistries)
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() error {
				return setManagedClusterImageRegistriesAnnotation(
					f.HubRuntimeClient(),
					f.TestClusterName(),
					string(registriesJSON),
				)
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Make sure addon is configured")
			Eventually(func() error {
				agentDeploy, err := getAgentDeployment(f, addonInstallNamespace)
				if err != nil {
					return err
				}

				return expectAgentImage(agentDeploy, "quay.io/ocm/managed-serviceaccount:latest")
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())
		})

	})

func getManagedClusterAddon(c client.Client, clusterName string) (*addonv1beta1.ManagedClusterAddOn, error) {
	addon := &addonv1beta1.ManagedClusterAddOn{}
	err := c.Get(context.TODO(), types.NamespacedName{
		Namespace: clusterName,
		Name:      common.AddonName,
	}, addon)
	return addon, err
}

func waitManagedClusterAddonAvailable(f framework.Framework) {
	Eventually(func() error {
		addon, err := getManagedClusterAddon(f.HubRuntimeClient(), f.TestClusterName())
		if err != nil {
			return err
		}
		if !meta.IsStatusConditionTrue(addon.Status.Conditions, addonv1beta1.ManagedClusterAddOnConditionAvailable) {
			return fmt.Errorf("addon is unavailable: %v", addon.Status.Conditions)
		}
		return nil
	}).WithTimeout(installWaitTimeout).Should(Succeed())
}

func setManagedClusterAddonConfigs(c client.Client, clusterName string, configs []addonv1beta1.AddOnConfig) error {
	addon, err := getManagedClusterAddon(c, clusterName)
	if err != nil {
		return err
	}
	addon.Spec.Configs = configs
	return c.Update(context.TODO(), addon)
}

func addonDeploymentConfigReference(namespace, name string) addonv1beta1.AddOnConfig {
	return addonv1beta1.AddOnConfig{
		ConfigGroupResource: addonv1beta1.ConfigGroupResource{
			Group:    addonDeploymentConfigGroup,
			Resource: addonDeploymentConfigResource,
		},
		ConfigReferent: addonv1beta1.ConfigReferent{
			Namespace: namespace,
			Name:      name,
		},
	}
}

func deleteAddOnDeploymentConfig(c client.Client, namespace, name string) error {
	return client.IgnoreNotFound(c.Delete(context.TODO(), &addonv1beta1.AddOnDeploymentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}))
}

func setManagedClusterImageRegistriesAnnotation(c client.Client, clusterName, value string) error {
	cluster := &clusterv1.ManagedCluster{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: clusterName}, cluster); err != nil {
		return err
	}

	clusterCopy := cluster.DeepCopy()
	annotations := maps.Clone(cluster.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[clusterv1.ClusterImageRegistriesAnnotationKey] = value
	clusterCopy.Annotations = annotations
	return c.Update(context.TODO(), clusterCopy)
}

func restoreManagedClusterImageRegistriesAnnotation(c client.Client, clusterName, value string, exists bool) error {
	cluster := &clusterv1.ManagedCluster{}
	if err := c.Get(context.TODO(), types.NamespacedName{Name: clusterName}, cluster); err != nil {
		return err
	}

	clusterCopy := cluster.DeepCopy()
	annotations := maps.Clone(cluster.Annotations)
	if exists {
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[clusterv1.ClusterImageRegistriesAnnotationKey] = value
	} else {
		delete(annotations, clusterv1.ClusterImageRegistriesAnnotationKey)
	}
	if len(annotations) == 0 {
		annotations = nil
	}
	clusterCopy.Annotations = annotations
	return c.Update(context.TODO(), clusterCopy)
}

func getDeployment(c kubernetes.Interface, namespace, name string) (*appsv1.Deployment, error) {
	return c.AppsV1().Deployments(namespace).Get(
		context.TODO(), name, metav1.GetOptions{})
}

func getAgentDeployment(f framework.Framework, namespace string) (*appsv1.Deployment, error) {
	return getDeployment(f.AgentNativeClient(), namespace, agentDeploymentName)
}

func waitAgentDeploymentRolledOutInAddonNamespace(f framework.Framework, validate func(*appsv1.Deployment) error) {
	Eventually(func() error {
		addon, err := getManagedClusterAddon(f.HubRuntimeClient(), f.TestClusterName())
		if err != nil {
			return err
		}
		if addon.Status.Namespace == "" {
			return fmt.Errorf("addon status namespace is empty")
		}
		deploy, err := getAgentDeployment(f, addon.Status.Namespace)
		if err != nil {
			return err
		}
		if err := agentDeploymentRolledOut(deploy); err != nil {
			return err
		}
		return validate(deploy)
	}).WithTimeout(installWaitTimeout).Should(Succeed())
}

func waitAgentDeploymentRolledOut(f framework.Framework, namespace string, validate func(*appsv1.Deployment) error) {
	Eventually(func() error {
		deploy, err := getAgentDeployment(f, namespace)
		if err != nil {
			return err
		}
		if err := agentDeploymentRolledOut(deploy); err != nil {
			return err
		}
		return validate(deploy)
	}).WithTimeout(installWaitTimeout).Should(Succeed())
}

func agentDeploymentRolledOut(deploy *appsv1.Deployment) error {
	replicas := int32(1)
	if deploy.Spec.Replicas != nil {
		replicas = *deploy.Spec.Replicas
	}
	if deploy.Generation > deploy.Status.ObservedGeneration {
		return fmt.Errorf("deployment generation %d has not been observed, status observed generation is %d",
			deploy.Generation, deploy.Status.ObservedGeneration)
	}
	if deploy.Status.UpdatedReplicas != replicas ||
		deploy.Status.ReadyReplicas != replicas ||
		deploy.Status.AvailableReplicas != replicas ||
		deploy.Status.UnavailableReplicas != 0 {
		return fmt.Errorf("deployment %s is not rolled out: %v", deploy.Name, deploy.Status)
	}
	return nil
}

func expectAgentImage(deploy *appsv1.Deployment, image string) error {
	containers := deploy.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		return fmt.Errorf("expect one container, but %v", containers)
	}
	if containers[0].Image != image {
		return fmt.Errorf("unexpected image %s", containers[0].Image)
	}
	return nil
}

func expectAgentPlacement(deploy *appsv1.Deployment, nodeSelector map[string]string, tolerations []corev1.Toleration) error {
	if !equality.Semantic.DeepEqual(deploy.Spec.Template.Spec.NodeSelector, nodeSelector) {
		return fmt.Errorf("unexpected nodeSelector %v", deploy.Spec.Template.Spec.NodeSelector)
	}
	if !equality.Semantic.DeepEqual(deploy.Spec.Template.Spec.Tolerations, tolerations) {
		return fmt.Errorf("unexpected tolerations %v", deploy.Spec.Template.Spec.Tolerations)
	}
	return nil
}

func appendManagedKubeConfigVariables(
	f framework.Framework,
	spec *addonv1beta1.AddOnDeploymentConfigSpec,
) {
	if !f.IsHostedMode() {
		return
	}

	spec.CustomizedVariables = append(spec.CustomizedVariables, addonv1beta1.CustomizedVariable{
		Name:  "managedKubeConfigProvisionerSyncInterval",
		Value: hostedProvisionerSyncInterval,
	})
	if namespace := f.ExternalManagedKubeConfigNamespace(); namespace != "" {
		spec.CustomizedVariables = append(spec.CustomizedVariables, addonv1beta1.CustomizedVariable{
			Name:  "externalManagedKubeConfigNamespace",
			Value: namespace,
		})
	}
	if secret := f.ExternalManagedKubeConfigSecret(); secret != "" {
		spec.CustomizedVariables = append(spec.CustomizedVariables, addonv1beta1.CustomizedVariable{
			Name:  "externalManagedKubeConfigSecret",
			Value: secret,
		})
	}
}

func seedExternalManagedKubeConfigSecret(f framework.Framework) {
	namespace := f.ExternalManagedKubeConfigNamespace()
	secret := f.ExternalManagedKubeConfigSecret()
	if namespace == "" {
		namespace = f.TestClusterName()
	}
	if secret == "" {
		secret = provisioner.DefaultExternalManagedKubeConfigSecret
	}

	kubeconfig, err := os.ReadFile(f.SpokeKubeConfigPath())
	Expect(err).NotTo(HaveOccurred())

	c := f.AgentRuntimeClient()

	Eventually(func() error {
		return ensureNamespace(context.TODO(), c, namespace)
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).ShouldNot(HaveOccurred())

	Eventually(func() error {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secret, Namespace: namespace}}
		_, err := controllerutil.CreateOrUpdate(context.TODO(), c, s, func() error {
			s.Type = corev1.SecretTypeOpaque
			if s.Data == nil {
				s.Data = map[string][]byte{}
			}
			s.Data[provisioner.KubeconfigSecretKey] = kubeconfig
			return nil
		})
		return err
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).ShouldNot(HaveOccurred())
}

func ensureNamespace(ctx context.Context, c client.Client, name string) error {
	return client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
}

func ensureHostedManagedClusterAddOn(f framework.Framework) {
	hostingClusterName := f.HostingClusterName()
	installNamespace := f.HostedInstallNamespace()

	c := f.HubRuntimeClient()
	Eventually(func() error {
		addon := &addonv1beta1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      common.AddonName,
				Namespace: f.TestClusterName(),
			},
		}
		_, err := controllerutil.CreateOrUpdate(context.TODO(), c, addon, func() error {
			if addon.Annotations == nil {
				addon.Annotations = map[string]string{}
			}
			addon.Annotations[addonv1beta1.HostingClusterNameAnnotationKey] = hostingClusterName
			addon.Annotations[addonv1beta1.InstallNamespaceAnnotation] = installNamespace
			return nil
		})
		return err
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).ShouldNot(HaveOccurred())
}

func waitForHostedAddonInstallNamespace(f framework.Framework, timeout time.Duration) {
	c := f.HubRuntimeClient()
	Eventually(func() error {
		addon, err := getManagedClusterAddon(c, f.TestClusterName())
		if err != nil {
			return err
		}
		if addon.Status.Namespace != f.HostedInstallNamespace() {
			return fmt.Errorf("addon install namespace is %q, want %q",
				addon.Status.Namespace, f.HostedInstallNamespace())
		}
		return nil
	}).WithTimeout(timeout).WithPolling(installPollInterval).ShouldNot(HaveOccurred())
}

func ensureHostedAddOnDeploymentConfig(f framework.Framework) {
	c := f.HubRuntimeClient()
	Eventually(func() error {
		deployConfig := &addonv1beta1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hostedAgentDeployConfigName,
				Namespace: f.TestClusterName(),
			},
		}
		_, err := controllerutil.CreateOrUpdate(context.TODO(), c, deployConfig, func() error {
			deployConfig.Spec = addonv1beta1.AddOnDeploymentConfigSpec{
				AgentInstallNamespace: f.HostedInstallNamespace(),
			}
			appendManagedKubeConfigVariables(f, &deployConfig.Spec)
			return nil
		})
		return err
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).ShouldNot(HaveOccurred())

	Eventually(func() error {
		addon, err := getManagedClusterAddon(c, f.TestClusterName())
		if err != nil {
			return err
		}

		desired := addonDeploymentConfigReference(f.TestClusterName(), hostedAgentDeployConfigName)
		for _, cfg := range addon.Spec.Configs {
			if cfg == desired {
				return nil
			}
		}
		addon.Spec.Configs = append(addon.Spec.Configs, desired)
		return c.Update(context.TODO(), addon)
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).ShouldNot(HaveOccurred())
}

func waitForHostedAddonRollout(f framework.Framework, rolloutStartedAt time.Time) {
	waitForHostedAddonInstallNamespace(f, hostedRolloutTimeout)
	addonInstallNamespace := f.HostedInstallNamespace()
	waitForHostedProvisionerSyncInterval(f, addonInstallNamespace)
	waitDeploymentRolledOut(f, addonInstallNamespace, provisionerDeploymentName, hostedRolloutTimeout)
	waitDeploymentRolledOut(f, addonInstallNamespace, agentDeploymentName, hostedRolloutTimeout)
	assertNoSpokeAgentWorkloads(f)

	waitForManagedAddonLease(f, addonInstallNamespace, rolloutStartedAt)
	waitManagedClusterAddonAvailable(f)
}

// waitForHostedProvisionerSyncInterval waits until the AddOnDeploymentConfig
// applied by the suite reaches the rendered provisioner arguments. Rollout
// completion alone does not prove that: the install namespace also resolves
// through the ManagedClusterAddOn annotation, so a provisioner rendered before
// the config landed still rolls out, with the default sync interval.
func waitForHostedProvisionerSyncInterval(f framework.Framework, namespace string) {
	agentClient := f.AgentNativeClient()
	expectedArg := "--sync-interval=" + hostedProvisionerSyncInterval
	Eventually(func() error {
		deploy, err := getDeployment(agentClient, namespace, provisionerDeploymentName)
		if err != nil {
			return err
		}
		args := deploy.Spec.Template.Spec.Containers[0].Args
		for _, arg := range args {
			if arg == expectedArg {
				return nil
			}
		}
		return fmt.Errorf("provisioner args %v do not yet include %q", args, expectedArg)
	}).WithTimeout(hostedRolloutTimeout).WithPolling(installPollInterval).Should(Succeed())
}

func waitDeploymentRolledOut(f framework.Framework, namespace, name string, timeout time.Duration) {
	agentClient := f.AgentNativeClient()
	Eventually(func() error {
		deploy, err := getDeployment(agentClient, namespace, name)
		if err != nil {
			return err
		}
		return agentDeploymentRolledOut(deploy)
	}).WithTimeout(timeout).WithPolling(installPollInterval).Should(Succeed())
}

func assertNoSpokeAgentWorkloads(f framework.Framework) {
	spokeClient := f.SpokeNativeClient()
	listOptions := metav1.ListOptions{
		LabelSelector: "addon-agent in (managed-serviceaccount,managed-serviceaccount-kubeconfig-provisioner)",
	}
	Consistently(func() error {
		deployments, err := spokeClient.AppsV1().Deployments("").List(context.TODO(), listOptions)
		if err != nil {
			return err
		}
		pods, err := spokeClient.CoreV1().Pods("").List(context.TODO(), listOptions)
		if err != nil {
			return err
		}

		names := make([]string, 0, len(deployments.Items)+len(pods.Items))
		for _, deploy := range deployments.Items {
			names = append(names, "deployment "+deploy.Namespace+"/"+deploy.Name)
		}
		for _, pod := range pods.Items {
			names = append(names, "pod "+pod.Namespace+"/"+pod.Name)
		}
		if len(names) != 0 {
			return fmt.Errorf("hosted addon workloads unexpectedly present on managed cluster: %v", names)
		}
		return nil
	}).WithTimeout(30 * time.Second).WithPolling(installPollInterval).Should(Succeed())
}

func verifyHostedManagedTokenRotation(f framework.Framework) {
	ctx := context.TODO()
	waitForHostedAddonInstallNamespace(f, installWaitTimeout)
	installNamespace := f.HostedInstallNamespace()
	hubClient := f.HubRuntimeClient()
	hostingClient := f.AgentNativeClient()
	managedClient := f.SpokeNativeClient()

	agentPod := waitForHostedAgentPod(hostingClient, installNamespace)
	agentPodName := agentPod.Name
	agentPodUID := agentPod.UID

	managedServiceAccount, err := managedClient.CoreV1().ServiceAccounts(installNamespace).Get(
		ctx, provisioner.DefaultManagedServiceAccountName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	previousServiceAccountUID := managedServiceAccount.UID

	managedSecretName := common.AddonName + managedKubeConfigSecretSuffix
	managedSecret, err := hostingClient.CoreV1().Secrets(installNamespace).Get(
		ctx, managedSecretName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	previousToken := slices.Clone(managedSecret.Data[corev1.ServiceAccountTokenKey])
	Expect(previousToken).NotTo(BeEmpty())

	By("Recreate the managed-cluster service account backing the hosted agent token")
	Expect(managedClient.CoreV1().ServiceAccounts(installNamespace).Delete(
		ctx,
		provisioner.DefaultManagedServiceAccountName,
		metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &previousServiceAccountUID}},
	)).To(Succeed())

	var currentServiceAccountUID types.UID
	Eventually(func() error {
		serviceAccount, err := managedClient.CoreV1().ServiceAccounts(installNamespace).Create(
			ctx,
			&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
				Name:      provisioner.DefaultManagedServiceAccountName,
				Namespace: installNamespace,
			}},
			metav1.CreateOptions{},
		)
		if apierrors.IsAlreadyExists(err) {
			serviceAccount, err = managedClient.CoreV1().ServiceAccounts(installNamespace).Get(
				ctx, provisioner.DefaultManagedServiceAccountName, metav1.GetOptions{})
		}
		if err != nil {
			return err
		}
		if serviceAccount.UID == previousServiceAccountUID {
			return fmt.Errorf("managed serviceaccount %s/%s still has UID %s",
				installNamespace, provisioner.DefaultManagedServiceAccountName, previousServiceAccountUID)
		}
		currentServiceAccountUID = serviceAccount.UID
		return nil
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).Should(Succeed())

	Eventually(func() error {
		secret, err := hostingClient.CoreV1().Secrets(installNamespace).Get(
			ctx, managedSecretName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if secret.Annotations[provisioner.ManagedServiceAccountUIDAnnotation] != string(currentServiceAccountUID) {
			return fmt.Errorf("managed kubeconfig secret still references serviceaccount UID %q",
				secret.Annotations[provisioner.ManagedServiceAccountUIDAnnotation])
		}
		if slices.Equal(secret.Data[corev1.ServiceAccountTokenKey], previousToken) {
			return fmt.Errorf("managed kubeconfig token has not rotated")
		}
		return nil
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).Should(Succeed())

	By("Reconcile a new ManagedServiceAccount through the unchanged hosted agent pod")
	probeName := "e2e-hosted-tokenfile-" + framework.RunID
	probe := &authv1beta1.ManagedServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: probeName, Namespace: f.TestClusterName()},
		Spec: authv1beta1.ManagedServiceAccountSpec{
			Rotation: authv1beta1.ManagedServiceAccountRotation{
				Validity: metav1.Duration{Duration: 30 * time.Minute},
			},
		},
	}
	Expect(hubClient.Create(ctx, probe)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(hubClient.Delete(context.TODO(), probe))).To(Succeed())
	})

	Eventually(func() error {
		_, err := managedClient.CoreV1().ServiceAccounts(installNamespace).Get(
			ctx, probeName, metav1.GetOptions{})
		return err
	}).WithTimeout(3 * time.Minute).WithPolling(installPollInterval).Should(Succeed())

	agentPod, err = hostingClient.CoreV1().Pods(installNamespace).Get(ctx, agentPodName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	Expect(agentPod.UID).To(Equal(agentPodUID), "hosted agent should reload tokenFile without restarting")
}

func verifyHostedManagedClusterHealth(f framework.Framework) {
	ctx := context.TODO()
	waitForHostedAddonInstallNamespace(f, installWaitTimeout)
	installNamespace := f.HostedInstallNamespace()
	hostingClient := f.AgentNativeClient()
	hubClient := f.HubRuntimeClient()
	agentPodUID := waitForHostedAgentPod(hostingClient, installNamespace).UID
	managedSecretName := common.AddonName + managedKubeConfigSecretSuffix

	managedSecret, err := hostingClient.CoreV1().Secrets(installNamespace).Get(
		ctx, managedSecretName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	originalToken := slices.Clone(managedSecret.Data[corev1.ServiceAccountTokenKey])
	Expect(originalToken).NotTo(BeEmpty())

	restoreToken := func() {
		setManagedKubeConfigToken(hostingClient, installNamespace, managedSecretName, originalToken)
	}
	DeferCleanup(func() {
		restoreToken()
		Eventually(func() error {
			lease, err := hostingClient.CoordinationV1().Leases(installNamespace).Get(
				context.TODO(), common.AddonName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if lease.Spec.RenewTime == nil {
				return fmt.Errorf("restored lease has no renew time")
			}
			return nil
		}).WithTimeout(hostedRolloutTimeout).WithPolling(installPollInterval).Should(Succeed())
		waitManagedClusterAddonAvailable(f)
	})

	By("Invalidate the managed cluster token mounted by the hosted agent")
	setManagedKubeConfigToken(
		hostingClient,
		installNamespace,
		managedSecretName,
		[]byte("invalid-e2e-token-"+framework.RunID),
	)
	waitForHostedAgentToObserveInvalidToken(hostingClient, installNamespace)

	By("Delete the stale lease to surface stopped renewal without waiting for the OCM lease grace period")
	staleLease, err := hostingClient.CoordinationV1().Leases(installNamespace).Get(
		ctx, common.AddonName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	staleLeaseUID := staleLease.UID
	Expect(hostingClient.CoordinationV1().Leases(installNamespace).Delete(
		ctx,
		common.AddonName,
		metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &staleLeaseUID}},
	)).To(Succeed())

	Consistently(func() error {
		_, err := hostingClient.CoordinationV1().Leases(installNamespace).Get(
			ctx, common.AddonName, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		default:
			return fmt.Errorf("managed addon lease was recreated while the managed token was invalid")
		}
	}).WithTimeout(hostedLeaseAbsenceWindow).WithPolling(installPollInterval).Should(Succeed())

	Eventually(func() error {
		addon, err := getManagedClusterAddon(hubClient, f.TestClusterName())
		if err != nil {
			return err
		}
		available := meta.FindStatusCondition(addon.Status.Conditions, addonv1beta1.ManagedClusterAddOnConditionAvailable)
		if available == nil || available.Status != metav1.ConditionUnknown ||
			available.Reason != addonv1beta1.AddonAvailableReasonLeaseLeaseNotFound {
			return fmt.Errorf("expected missing lease to make addon availability unknown, got %v", available)
		}
		return nil
	}).WithTimeout(hostedRolloutTimeout).WithPolling(installPollInterval).Should(Succeed())

	By("Restore the managed token and verify lease health recovers without restarting the agent")
	restoreToken()
	Eventually(func() error {
		lease, err := hostingClient.CoordinationV1().Leases(installNamespace).Get(
			ctx, common.AddonName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if lease.UID == staleLeaseUID {
			return fmt.Errorf("managed addon lease has not been recreated")
		}
		return nil
	}).WithTimeout(hostedRolloutTimeout).WithPolling(installPollInterval).Should(Succeed())
	waitManagedClusterAddonAvailable(f)
	Expect(waitForHostedAgentPod(hostingClient, installNamespace).UID).To(Equal(agentPodUID),
		"hosted agent should recover managed API health without restarting")
}

func waitForHostedAgentPod(hostingClient kubernetes.Interface, namespace string) *corev1.Pod {
	var agentPod *corev1.Pod
	Eventually(func() error {
		pods, err := hostingClient.CoreV1().Pods(namespace).List(
			context.TODO(), metav1.ListOptions{LabelSelector: "addon-agent=managed-serviceaccount"})
		if err != nil {
			return err
		}
		if len(pods.Items) != 1 {
			return fmt.Errorf("expected exactly one hosted agent pod, found %d", len(pods.Items))
		}
		agentPod = pods.Items[0].DeepCopy()
		return nil
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).Should(Succeed())
	return agentPod
}

func setManagedKubeConfigToken(
	hostingClient kubernetes.Interface,
	namespace, secretName string,
	token []byte,
) {
	Eventually(func() error {
		secret, err := hostingClient.CoreV1().Secrets(namespace).Get(
			context.TODO(), secretName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		secret.Data[corev1.ServiceAccountTokenKey] = slices.Clone(token)
		_, err = hostingClient.CoreV1().Secrets(namespace).Update(
			context.TODO(), secret, metav1.UpdateOptions{})
		return err
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).Should(Succeed())
}

// waitForHostedAgentToObserveInvalidToken uses a stopped renewal only as a
// readiness signal for Secret volume propagation. The caller separately
// deletes the Lease and proves that the agent cannot recreate it.
func waitForHostedAgentToObserveInvalidToken(hostingClient kubernetes.Interface, namespace string) {
	var observedRenewTime time.Time
	var unchangedSince time.Time
	Eventually(func() error {
		lease, err := hostingClient.CoordinationV1().Leases(namespace).Get(
			context.TODO(), common.AddonName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if lease.Spec.RenewTime == nil {
			return fmt.Errorf("lease %s/%s has no renew time", namespace, common.AddonName)
		}
		renewTime := lease.Spec.RenewTime.Time
		if unchangedSince.IsZero() || !renewTime.Equal(observedRenewTime) {
			observedRenewTime = renewTime
			unchangedSince = time.Now()
			return fmt.Errorf("lease renewal has not yet remained stopped for %s", hostedTokenPropagationWindow)
		}
		if stableFor := time.Since(unchangedSince); stableFor < hostedTokenPropagationWindow {
			return fmt.Errorf("lease renewal has remained stopped for %s, want %s", stableFor, hostedTokenPropagationWindow)
		}
		return nil
	}).WithTimeout(hostedRolloutTimeout).WithPolling(installPollInterval).Should(Succeed())
}

func waitForManagedAddonLease(f framework.Framework, namespace string, rolloutStartedAt time.Time) {
	leaseClient := f.AgentNativeClient()
	Eventually(func() error {
		lease, err := leaseClient.CoordinationV1().Leases(namespace).Get(
			context.TODO(), common.AddonName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if lease.Spec.RenewTime == nil {
			return fmt.Errorf("lease %s/%s has no renew time", namespace, common.AddonName)
		}
		if lease.Spec.RenewTime.Time.Before(rolloutStartedAt) {
			return fmt.Errorf("lease %s/%s has not been renewed during this rollout", namespace, common.AddonName)
		}
		if lease.Spec.LeaseDurationSeconds == nil || *lease.Spec.LeaseDurationSeconds <= 0 {
			return fmt.Errorf("lease %s/%s has no valid duration", namespace, common.AddonName)
		}
		expiresAt := lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
		if !expiresAt.After(time.Now()) {
			return fmt.Errorf("lease %s/%s expired at %s", namespace, common.AddonName, expiresAt)
		}
		// The addon-framework lease updater does not set holderIdentity; a fresh,
		// unexpired renewal is the active-agent signal for this lease.
		return nil
	}).WithTimeout(installWaitTimeout).WithPolling(installPollInterval).ShouldNot(HaveOccurred())
}
