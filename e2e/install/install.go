package install

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const installTestBasename = "install"

var _ = Describe("Addon Installation Test", Label("install"),
	func() {
		f := framework.NewE2EFramework(installTestBasename)
		It("Addon healthiness should work", func() {
			Eventually(func() (bool, error) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      common.AddonName,
				}, addon)
				Expect(err).NotTo(HaveOccurred())
				return meta.IsStatusConditionTrue(addon.Status.Conditions, addonv1alpha1.ManagedClusterAddOnConditionAvailable), nil
			}).WithTimeout(time.Minute).Should(BeTrue())
		})

		It("Addon should can be configured with AddOnDeployMentConfig", func() {
			deployConfigName := "tolerations-deploy-config"
			nodeSelector := map[string]string{"kubernetes.io/os": "linux"}
			tolerations := []corev1.Toleration{{Key: "node-role.kubernetes.io/infra", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}

			c := f.HubRuntimeClient()
			By("Prepare a AddOnDeploymentConfig for managed-serviceaccount addon")
			Eventually(func() error {
				err := c.Create(context.TODO(), &addonv1alpha1.AddOnDeploymentConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      deployConfigName,
						Namespace: f.TestClusterName(),
					},
					Spec: addonv1alpha1.AddOnDeploymentConfigSpec{
						NodePlacement: &addonv1alpha1.NodePlacement{
							NodeSelector: nodeSelector,
							Tolerations:  tolerations,
						},
					},
				})
				// Ignore already exists error so it is easier to debug locally
				if errors.IsAlreadyExists(err) {
					return nil
				}

				return err
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Add the config to managed-serviceaccount addon")
			Eventually(func() error {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				if err := c.Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      "managed-serviceaccount",
				}, addon); err != nil {
					return err
				}

				addon.Spec.Configs = []addonv1alpha1.AddOnConfig{
					{
						ConfigGroupResource: addonv1alpha1.ConfigGroupResource{
							Group:    "addon.open-cluster-management.io",
							Resource: "addondeploymentconfigs",
						},
						ConfigReferent: addonv1alpha1.ConfigReferent{
							Namespace: f.TestClusterName(),
							Name:      deployConfigName,
						},
					},
				}

				return c.Update(context.TODO(), addon)
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Ensure the config is referenced")
			Eventually(func() error {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
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
					if ref.Name != deployConfigName {
						return fmt.Errorf("unexpected config references %v", addon.Status.ConfigReferences)
					}
					found = true
				}
				if !found {
					return fmt.Errorf("no config references in addon status")
				}

				return nil
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Ensure the managed serviceaccount addon agent is configured")
			Eventually(func() error {
				deployments := &appsv1.DeploymentList{}
				c.List(context.TODO(), deployments, &client.ListOptions{
					FieldSelector: fields.SelectorFromSet(map[string]string{
						"metadata.name": "managed-serviceaccount-addon-agent",
					}),
				})

				if len(deployments.Items) != 1 {
					return fmt.Errorf("unexpected number of deployments %v", deployments.Items)
				}
				deploy := &deployments.Items[0]
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
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				if err := c.Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      "managed-serviceaccount",
				}, addon); err != nil {
					return err
				}

				if !meta.IsStatusConditionTrue(
					addon.Status.Conditions,
					addonv1alpha1.ManagedClusterAddOnConditionAvailable) {
					return fmt.Errorf("addon is unavailable")
				}

				return nil
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())
		})

		It("Agent image should be overrode by cluster annotation", func() {
			By("Get Addon agent install namespace")
			addon := &addonv1alpha1.ManagedClusterAddOn{}
			err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
				Namespace: f.TestClusterName(),
				Name:      common.AddonName,
			}, addon)
			Expect(err).NotTo(HaveOccurred())
			addonInstallNamespace := addon.Spec.InstallNamespace

			By("Prepare cluster annotation for addon image override config")
			overrideRegistries := addonv1alpha1.AddOnDeploymentConfigSpec{
				Registries: []addonv1alpha1.ImageMirror{
					{
						Source: "quay.io/open-cluster-management",
						Mirror: "quay.io/ocm",
					},
				},
			}
			registriesJson, err := json.Marshal(overrideRegistries)
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
				annotations[clusterv1.ClusterImageRegistriesAnnotationKey] = string(registriesJson)

				newCluster.Annotations = annotations
				return f.HubRuntimeClient().Update(context.Background(), newCluster)
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Make sure addon is configured")
			Eventually(func() error {
				agentDeploy := &appsv1.Deployment{}
				agentDeploy, err := f.HubNativeClient().AppsV1().Deployments(addonInstallNamespace).Get(
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
				agentDeploy := &appsv1.Deployment{}
				agentDeploy, err := f.HubNativeClient().AppsV1().Deployments(addonInstallNamespace).Get(
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
