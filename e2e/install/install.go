package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/manager/provisioner"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const installTestBasename = "install"
const hostedModeDeployConfigName = "hosted-mode-config"
const defaultAddonInstallNamespace = "open-cluster-management-agent-addon"

// Seed hosted-mode prerequisites in a BeforeSuite so they run before any spec
// regardless of Ginkgo randomization. The helpers are no-ops outside hosted mode.
var _ = BeforeSuite(func() {
	f := framework.NewSuiteFramework(installTestBasename)

	By("Ensure hosted-mode ManagedClusterAddOn exists and is annotated if configured")
	ensureHostedManagedClusterAddOn(f)

	By("Apply hosted-mode AddOnDeploymentConfig if configured")
	ensureHostedAddOnDeploymentConfig(f)

	By("Seed external managed kubeconfig secret for hosted mode if configured")
	seedExternalManagedKubeConfigSecret(f)

	By("Wait for hosted addon rollout if configured")
	waitForHostedAddonRollout(f)
})

var _ = Describe("Addon Installation Test", Label("install"),
	func() {
		f := framework.NewE2EFramework(installTestBasename)
		It("Addon healthiness should work", func() {
			Eventually(func() (bool, error) {
				addon := &addonv1beta1.ManagedClusterAddOn{}
				err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      common.AddonName,
				}, addon)
				Expect(err).NotTo(HaveOccurred())
				return meta.IsStatusConditionTrue(addon.Status.Conditions, addonv1beta1.ManagedClusterAddOnConditionAvailable), nil
			}).WithTimeout(time.Minute).Should(BeTrue())
		})

		It("Addon should be configurable with AddOnDeploymentConfig", func() {
			deployConfigName := "tolerations-deploy-config"
			agentInstallNamespace := "managed-serviceaccount-config-test"
			expectedInstallNamespace := agentInstallNamespace
			if f.IsHostedMode() {
				expectedInstallNamespace = f.HostedInstallNamespace()
			}
			nodeSelector := map[string]string{"kubernetes.io/os": "linux"}
			tolerations := []corev1.Toleration{{Key: "node-role.kubernetes.io/infra", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}

			c := f.HubRuntimeClient()
			By("Prepare a AddOnDeploymentConfig for managed-serviceaccount addon")
			Eventually(func() error {
				deployConfigSpec := addonv1beta1.AddOnDeploymentConfigSpec{
					AgentInstallNamespace: agentInstallNamespace,
					NodePlacement: &addonv1beta1.NodePlacement{
						NodeSelector: nodeSelector,
						Tolerations:  tolerations,
					},
				}
				if f.IsHostedMode() {
					deployConfigSpec.AgentInstallNamespace = f.HostedInstallNamespace()
				}
				appendExternalManagedKubeConfigVariables(f, &deployConfigSpec)

				deployConfig := &addonv1beta1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      deployConfigName,
						Namespace: f.TestClusterName(),
					},
				}
				_, err := controllerutil.CreateOrUpdate(context.TODO(), c, deployConfig, func() error {
					deployConfig.Spec = deployConfigSpec
					return nil
				})
				return err
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Add the config to managed-serviceaccount addon")
			Eventually(func() error {
				addon := &addonv1beta1.ManagedClusterAddOn{}
				if err := c.Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      "managed-serviceaccount",
				}, addon); err != nil {
					return err
				}

				addon.Spec.Configs = []addonv1beta1.AddOnConfig{
					{
						ConfigGroupResource: addonv1beta1.ConfigGroupResource{
							Group:    "addon.open-cluster-management.io",
							Resource: "addondeploymentconfigs",
						},
						ConfigReferent: addonv1beta1.ConfigReferent{
							Namespace: f.TestClusterName(),
							Name:      deployConfigName,
						},
					},
				}

				return c.Update(context.TODO(), addon)
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Ensure the config is referenced")
			Eventually(func() error {
				addon := &addonv1beta1.ManagedClusterAddOn{}
				if err := c.Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      "managed-serviceaccount",
				}, addon); err != nil {
					return err
				}

				if len(addon.Status.ConfigReferences) == 0 {
					return fmt.Errorf("no config references in addon status")
				}
				found := false
				for _, ref := range addon.Status.ConfigReferences {
					if ref.Resource != "addondeploymentconfigs" {
						continue
					}
					if ref.Group != "addon.open-cluster-management.io" {
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
					if ref.LastObservedGeneration == 0 {
						return fmt.Errorf("last observed generation is empty in config references %v", addon.Status.ConfigReferences)
					}
					found = true
				}
				if !found {
					return fmt.Errorf("no config references in addon status")
				}
				if !meta.IsStatusConditionTrue(addon.Status.Conditions, addonv1beta1.ManagedClusterAddOnConditionConfigured) {
					return fmt.Errorf("addon is not configured: %v", addon.Status.Conditions)
				}
				if addon.Status.Namespace != expectedInstallNamespace {
					return fmt.Errorf("addon is installed in %q, want %q", addon.Status.Namespace, expectedInstallNamespace)
				}

				return nil
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Ensure the managed serviceaccount addon agent is configured")
			Eventually(func() error {
				addon := &addonv1beta1.ManagedClusterAddOn{}
				if err := c.Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      common.AddonName,
				}, addon); err != nil {
					return err
				}
				addonInstallNamespace := addon.Status.Namespace
				if addonInstallNamespace == "" {
					return fmt.Errorf("addon install namespace not yet set")
				}

				deploy := &appsv1.Deployment{}
				if err := f.AgentRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: addonInstallNamespace,
					Name:      "managed-serviceaccount-addon-agent",
				}, deploy); err != nil {
					return err
				}

				if deploy.Status.AvailableReplicas != *deploy.Spec.Replicas {
					return fmt.Errorf("unexpected available replicas %v", deploy.Status)
				}

				if !equality.Semantic.DeepEqual(deploy.Spec.Template.Spec.NodeSelector, nodeSelector) {
					return fmt.Errorf("unexpected nodeSeletcor %v", deploy.Spec.Template.Spec.NodeSelector)
				}

				if !equality.Semantic.DeepEqual(deploy.Spec.Template.Spec.Tolerations, tolerations) {
					return fmt.Errorf("unexpected tolerations %v", deploy.Spec.Template.Spec.Tolerations)
				}
				return nil
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Ensure the managed-serviceaccountis available")
			Eventually(func() error {
				addon := &addonv1beta1.ManagedClusterAddOn{}
				if err := c.Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      "managed-serviceaccount",
				}, addon); err != nil {
					return err
				}

				if !meta.IsStatusConditionTrue(
					addon.Status.Conditions,
					addonv1beta1.ManagedClusterAddOnConditionAvailable) {
					return fmt.Errorf("addon is unavailable")
				}

				return nil
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())
		})

		It("Agent image should be overridden by cluster annotation", func() {
			By("Get Addon agent install namespace")
			addon := &addonv1beta1.ManagedClusterAddOn{}
			err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
				Namespace: f.TestClusterName(),
				Name:      common.AddonName,
			}, addon)
			Expect(err).NotTo(HaveOccurred())
			addonInstallNamespace := addon.Status.Namespace

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
				cluster := &clusterv1.ManagedCluster{}
				err := f.HubRuntimeClient().Get(context.Background(),
					types.NamespacedName{Name: f.TestClusterName()},
					cluster)
				if err != nil {
					return err
				}

				newCluster := cluster.DeepCopy()

				annotations := cluster.Annotations
				if annotations == nil {
					annotations = make(map[string]string)
				}
				annotations[clusterv1.ClusterImageRegistriesAnnotationKey] = string(registriesJSON)

				newCluster.Annotations = annotations
				return f.HubRuntimeClient().Update(context.Background(), newCluster)
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Make sure addon is configured")
			Eventually(func() error {
				agentDeploy, err := f.AgentNativeClient().AppsV1().Deployments(addonInstallNamespace).Get(
					context.Background(), "managed-serviceaccount-addon-agent", metav1.GetOptions{})
				if err != nil {
					return err
				}

				containers := agentDeploy.Spec.Template.Spec.Containers
				if len(containers) != 1 {
					return fmt.Errorf("expect one container, but %v", containers)
				}

				if containers[0].Image != "quay.io/ocm/managed-serviceaccount:latest" {
					return fmt.Errorf("unexpected image %s", containers[0].Image)
				}

				return nil
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			// restore the image override config, because the override image is not available
			// but it is needed by the pre-delete job
			By("Restore the managed cluster annotation")
			Eventually(func() error {
				cluster := &clusterv1.ManagedCluster{}
				err := f.HubRuntimeClient().Get(context.Background(),
					types.NamespacedName{Name: f.TestClusterName()},
					cluster)
				if err != nil {
					return err
				}

				newCluster := cluster.DeepCopy()
				delete(newCluster.Annotations, clusterv1.ClusterImageRegistriesAnnotationKey)
				return f.HubRuntimeClient().Update(context.Background(), newCluster)
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Make sure addon config is restored")
			Eventually(func() error {
				agentDeploy, err := f.AgentNativeClient().AppsV1().Deployments(addonInstallNamespace).Get(
					context.Background(), "managed-serviceaccount-addon-agent", metav1.GetOptions{})
				if err != nil {
					return err
				}

				containers := agentDeploy.Spec.Template.Spec.Containers
				if len(containers) != 1 {
					return fmt.Errorf("expect one container, but %v", containers)
				}

				if containers[0].Image != "quay.io/open-cluster-management/managed-serviceaccount:latest" {
					return fmt.Errorf("unexpected image %s", containers[0].Image)
				}

				return nil
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())
		})

	})

func appendExternalManagedKubeConfigVariables(
	f framework.Framework,
	spec *addonv1beta1.AddOnDeploymentConfigSpec,
) {
	if namespace := f.ExternalManagedKubeConfigNamespace(); namespace != "" {
		spec.CustomizedVariables = append(spec.CustomizedVariables, addonv1beta1.CustomizedVariable{
			Name:  "ExternalManagedKubeConfigNamespace",
			Value: namespace,
		})
	}
	if secret := f.ExternalManagedKubeConfigSecret(); secret != "" {
		spec.CustomizedVariables = append(spec.CustomizedVariables, addonv1beta1.CustomizedVariable{
			Name:  "ExternalManagedKubeConfigSecret",
			Value: secret,
		})
	}
}

// skipExternalManagedKubeConfig reports whether the hosted-mode external managed kubeconfig prerequisites are absent.
func skipExternalManagedKubeConfig(f framework.Framework) bool {
	return !f.IsHostedMode() &&
		f.ExternalManagedKubeConfigNamespace() == "" &&
		f.ExternalManagedKubeConfigSecret() == ""
}

func seedExternalManagedKubeConfigSecret(f framework.Framework) {
	if skipExternalManagedKubeConfig(f) {
		return
	}
	namespace := f.ExternalManagedKubeConfigNamespace()
	secret := f.ExternalManagedKubeConfigSecret()
	if namespace == "" {
		namespace = f.TestClusterName()
	}
	if secret == "" {
		secret = provisioner.DefaultExternalManagedKubeConfigSecret
	}

	kubeconfigPath := f.SpokeKubeConfigPath()
	Expect(kubeconfigPath).NotTo(BeEmpty(), "--spoke-kubeconfig is required to seed the hosted-mode external managed kubeconfig secret")
	kubeconfig, err := os.ReadFile(kubeconfigPath)
	Expect(err).NotTo(HaveOccurred())

	c := f.AgentRuntimeClient()

	Eventually(func() error {
		return ensureNamespace(context.TODO(), c, namespace)
	}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

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
	}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())
}

func ensureNamespace(ctx context.Context, c client.Client, name string) error {
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// ensureHostedManagedClusterAddOn annotates the ManagedClusterAddOn for hosted
// placement. It is a no-op outside hosted mode.
func ensureHostedManagedClusterAddOn(f framework.Framework) {
	if !f.IsHostedMode() {
		return
	}

	hostingClusterName := f.HostingClusterName()
	installNamespace := f.HostedInstallNamespace()
	Expect(hostingClusterName).NotTo(BeEmpty(), "--hosting-cluster-name is required for hosted-mode e2e")
	Expect(installNamespace).NotTo(BeEmpty(), "--hosted-install-namespace is required for hosted-mode e2e")

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
	}).WithTimeout(2 * time.Minute).ShouldNot(HaveOccurred())
}

func waitForAddonInstallNamespace(f framework.Framework) string {
	addonInstallNamespace := ""
	Eventually(func() error {
		addon := &addonv1beta1.ManagedClusterAddOn{}
		if err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
			Namespace: f.TestClusterName(),
			Name:      common.AddonName,
		}, addon); err != nil {
			return err
		}
		if addon.Status.Namespace == "" {
			return fmt.Errorf("addon install namespace not yet set")
		}
		if f.IsHostedMode() && f.HostedInstallNamespace() != "" && addon.Status.Namespace != f.HostedInstallNamespace() {
			return fmt.Errorf("addon install namespace is %q, want %q",
				addon.Status.Namespace, f.HostedInstallNamespace())
		}
		addonInstallNamespace = addon.Status.Namespace
		return nil
	}).WithTimeout(2 * time.Minute).ShouldNot(HaveOccurred())
	return addonInstallNamespace
}

func ensureHostedAddOnDeploymentConfig(f framework.Framework) {
	if skipExternalManagedKubeConfig(f) {
		return
	}

	c := f.HubRuntimeClient()
	Eventually(func() error {
		deployConfig := &addonv1beta1.AddOnDeploymentConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hostedModeDeployConfigName,
				Namespace: f.TestClusterName(),
			},
		}
		_, err := controllerutil.CreateOrUpdate(context.TODO(), c, deployConfig, func() error {
			deployConfig.Spec = addonv1beta1.AddOnDeploymentConfigSpec{}
			if f.IsHostedMode() {
				deployConfig.Spec.AgentInstallNamespace = f.HostedInstallNamespace()
			}
			appendExternalManagedKubeConfigVariables(f, &deployConfig.Spec)
			return nil
		})
		return err
	}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

	Eventually(func() error {
		addon := &addonv1beta1.ManagedClusterAddOn{}
		if err := c.Get(context.TODO(), types.NamespacedName{
			Namespace: f.TestClusterName(),
			Name:      common.AddonName,
		}, addon); err != nil {
			return err
		}

		desired := addonv1beta1.AddOnConfig{
			ConfigGroupResource: addonv1beta1.ConfigGroupResource{
				Group:    "addon.open-cluster-management.io",
				Resource: "addondeploymentconfigs",
			},
			ConfigReferent: addonv1beta1.ConfigReferent{
				Namespace: f.TestClusterName(),
				Name:      hostedModeDeployConfigName,
			},
		}
		for _, cfg := range addon.Spec.Configs {
			if cfg == desired {
				return nil
			}
		}
		addon.Spec.Configs = append(addon.Spec.Configs, desired)
		return c.Update(context.TODO(), addon)
	}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())
}

func waitForHostedAddonRollout(f framework.Framework) {
	if !f.IsHostedMode() {
		return
	}

	addonInstallNamespace := waitForAddonInstallNamespace(f)
	waitForAgentDeploymentAvailable(f, addonInstallNamespace, "managed-serviceaccount-kubeconfig-provisioner")
	waitForHostedManagedKubeConfigSecret(f, addonInstallNamespace)
	waitForAgentDeploymentAvailable(f, addonInstallNamespace, "managed-serviceaccount-addon-agent")
	deleteStaleSpokeAgentDeployments(f, addonInstallNamespace)

	waitForManagedAddonLease(f, addonInstallNamespace)
	waitForManagedClusterAddOnAvailable(f)
}

func waitForAgentDeploymentAvailable(f framework.Framework, namespace, name string) {
	Eventually(func() error {
		deployment, err := f.AgentNativeClient().AppsV1().Deployments(namespace).Get(
			context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if deployment.Spec.Replicas == nil {
			return fmt.Errorf("deployment %s/%s has nil replicas", namespace, name)
		}
		if deployment.Status.AvailableReplicas < *deployment.Spec.Replicas {
			return fmt.Errorf("deployment %s/%s available replicas %d/%d",
				namespace, name, deployment.Status.AvailableReplicas, *deployment.Spec.Replicas)
		}
		return nil
	}).WithTimeout(2 * time.Minute).ShouldNot(HaveOccurred())
}

func waitForHostedManagedKubeConfigSecret(f framework.Framework, namespace string) {
	secretName := common.AddonName + "-managed-kubeconfig"
	Eventually(func() error {
		secret, err := f.AgentNativeClient().CoreV1().Secrets(namespace).Get(
			context.TODO(), secretName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if len(secret.Data[provisioner.KubeconfigSecretKey]) == 0 {
			return fmt.Errorf("secret %s/%s has no %q data", namespace, secretName, provisioner.KubeconfigSecretKey)
		}
		return nil
	}).WithTimeout(2 * time.Minute).ShouldNot(HaveOccurred())
}

func deleteStaleSpokeAgentDeployments(f framework.Framework, addonInstallNamespace string) {
	deleteStaleSpokeAgentDeployment(f, addonInstallNamespace)
	if addonInstallNamespace != defaultAddonInstallNamespace {
		deleteStaleSpokeAgentDeployment(f, defaultAddonInstallNamespace)
	}
}

func deleteStaleSpokeAgentDeployment(f framework.Framework, namespace string) {
	deployments := f.SpokeNativeClient().AppsV1().Deployments(namespace)
	Eventually(func() error {
		err := deployments.Delete(context.TODO(), "managed-serviceaccount-addon-agent", metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}

		_, err = deployments.Get(context.TODO(), "managed-serviceaccount-addon-agent", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("stale spoke deployment %s/managed-serviceaccount-addon-agent still exists", namespace)
	}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())
}

func waitForManagedAddonLease(f framework.Framework, namespace string) {
	leaseClient := f.SpokeNativeClient()
	if f.IsHostedMode() {
		leaseClient = f.AgentNativeClient()
	}
	Eventually(func() error {
		_, err := leaseClient.CoordinationV1().Leases(namespace).Get(
			context.TODO(), common.AddonName, metav1.GetOptions{})
		return err
	}).WithTimeout(2 * time.Minute).ShouldNot(HaveOccurred())
}

func waitForManagedClusterAddOnAvailable(f framework.Framework) {
	Eventually(func() error {
		addon := &addonv1beta1.ManagedClusterAddOn{}
		if err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
			Namespace: f.TestClusterName(),
			Name:      common.AddonName,
		}, addon); err != nil {
			return err
		}
		if !meta.IsStatusConditionTrue(addon.Status.Conditions, addonv1beta1.ManagedClusterAddOnConditionAvailable) {
			return fmt.Errorf("managedclusteraddon %s/%s is not available", f.TestClusterName(), common.AddonName)
		}
		return nil
	}).WithTimeout(2 * time.Minute).ShouldNot(HaveOccurred())
}
