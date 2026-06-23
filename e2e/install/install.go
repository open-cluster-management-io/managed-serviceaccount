package install

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive,staticcheck // idiomatic ginkgo usage
	. "github.com/onsi/gomega"    //nolint:revive,staticcheck // idiomatic gomega usage

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const (
	installTestBasename           = "install"
	installWaitTimeout            = 2 * time.Minute
	agentDeploymentName           = "managed-serviceaccount-addon-agent"
	addonDeploymentConfigGroup    = "addon.open-cluster-management.io"
	addonDeploymentConfigResource = "addondeploymentconfigs"
	defaultAgentImage             = "quay.io/open-cluster-management/managed-serviceaccount:latest"
)

var _ = Describe("Addon Installation Test", Label("install"),
	func() {
		f := framework.NewE2EFramework(installTestBasename)
		It("Addon healthiness should work", func() {
			waitManagedClusterAddonAvailable(f)
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

			By("Prepare a AddOnDeploymentConfig for managed-serviceaccount addon")
			Eventually(func() error {
				deployConfigSpec := addonv1alpha1.AddOnDeploymentConfigSpec{
					NodePlacement: &addonv1alpha1.NodePlacement{
						NodeSelector: nodeSelector,
						Tolerations:  tolerations,
					},
				}

				deployConfig := &addonv1alpha1.AddOnDeploymentConfig{
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
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Add the config to managed-serviceaccount addon")
			Eventually(func() error {
				return setManagedClusterAddonConfigs(
					c,
					f.TestClusterName(),
					[]addonv1alpha1.AddOnConfig{addonDeploymentConfigReference(f.TestClusterName(), deployConfigName)},
				)
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Ensure the config is referenced")
			Eventually(func() error {
				addon, err := getManagedClusterAddon(c, f.TestClusterName())
				if err != nil {
					return err
				}

				for _, ref := range addon.Status.ConfigReferences {
					if ref.Group == addonDeploymentConfigGroup &&
						ref.Resource == addonDeploymentConfigResource &&
						ref.Namespace == f.TestClusterName() &&
						ref.Name == deployConfigName {
						return nil
					}
				}

				return fmt.Errorf("expected config reference %s/%s not found in %v", f.TestClusterName(), deployConfigName, addon.Status.ConfigReferences)
			}).WithTimeout(installWaitTimeout).ShouldNot(HaveOccurred())

			By("Ensure the managed serviceaccount addon agent is configured")
			waitAgentDeploymentRolledOut(f, addonInstallNamespace, func(deploy *appsv1.Deployment) error {
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
			overrideRegistries := addonv1alpha1.AddOnDeploymentConfigSpec{
				Registries: []addonv1alpha1.ImageMirror{
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

func getManagedClusterAddon(c client.Client, clusterName string) (*addonv1alpha1.ManagedClusterAddOn, error) {
	addon := &addonv1alpha1.ManagedClusterAddOn{}
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
		if !meta.IsStatusConditionTrue(addon.Status.Conditions, addonv1alpha1.ManagedClusterAddOnConditionAvailable) {
			return fmt.Errorf("addon is unavailable: %v", addon.Status.Conditions)
		}
		return nil
	}).WithTimeout(installWaitTimeout).Should(Succeed())
}

func setManagedClusterAddonConfigs(c client.Client, clusterName string, configs []addonv1alpha1.AddOnConfig) error {
	addon, err := getManagedClusterAddon(c, clusterName)
	if err != nil {
		return err
	}
	addon.Spec.Configs = configs
	return c.Update(context.TODO(), addon)
}

func addonDeploymentConfigReference(namespace, name string) addonv1alpha1.AddOnConfig {
	return addonv1alpha1.AddOnConfig{
		ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
			Group:    addonDeploymentConfigGroup,
			Resource: addonDeploymentConfigResource,
		},
		ConfigReferent: addonv1alpha1.ConfigReferent{
			Namespace: namespace,
			Name:      name,
		},
	}
}

func deleteAddOnDeploymentConfig(c client.Client, namespace, name string) error {
	return client.IgnoreNotFound(c.Delete(context.TODO(), &addonv1alpha1.AddOnDeploymentConfig{
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

func getAgentDeployment(f framework.Framework, namespace string) (*appsv1.Deployment, error) {
	return f.HubNativeClient().AppsV1().Deployments(namespace).Get(
		context.TODO(), agentDeploymentName, metav1.GetOptions{})
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
