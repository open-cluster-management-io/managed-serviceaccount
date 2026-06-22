package cleanup

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive,staticcheck // idiomatic ginkgo usage
	. "github.com/onsi/gomega"    //nolint:revive,staticcheck // idiomatic gomega usage

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	workv1 "open-cluster-management.io/api/work/v1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/provisioner"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const (
	cleanupTestBasename = "cleanup"
	cleanupWaitTimeout  = 5 * time.Minute
	cleanupPollInterval = 2 * time.Second

	agentDeploymentName       = "managed-serviceaccount-addon-agent"
	provisionerDeploymentName = "managed-serviceaccount-kubeconfig-provisioner"
)

var _ = Describe("Addon Cleanup Test", Label("cleanup"), func() {
	f := framework.NewE2EFramework(cleanupTestBasename)

	It("removes addon resources when the ManagedClusterAddOn is deleted", func(ctx SpecContext) {
		hubClient := f.HubRuntimeClient()
		addonKey := types.NamespacedName{
			Namespace: f.TestClusterName(),
			Name:      common.AddonName,
		}
		addon := &addonv1beta1.ManagedClusterAddOn{}
		Expect(hubClient.Get(ctx, addonKey, addon)).To(Succeed())
		Expect(addon.Status.Namespace).NotTo(BeEmpty(), "ManagedClusterAddOn status.namespace must be set before cleanup")
		installNamespace := addon.Status.Namespace

		verifyManifestWorksExist(ctx, f, hubClient)
		verifyCleanupPreconditions(ctx, f, installNamespace)
		if f.IsHostedMode() {
			verifyHostedCleanupPreconditions(ctx, f, installNamespace)
		}

		disableAutomaticInstallation(ctx, hubClient)

		By("Deleting the ManagedClusterAddOn")
		Expect(hubClient.Delete(ctx, addon)).To(Succeed())
		expectObjectDeleted(ctx, hubClient, addonKey, &addonv1beta1.ManagedClusterAddOn{})
		expectManifestWorksDeleted(ctx, f, hubClient)

		expectAddonResourcesDeleted(ctx, f, installNamespace)
		if f.IsHostedMode() {
			expectHostedResourcesDeleted(ctx, f, installNamespace)
		}
	})
})

// addonManifestWorkSelectors maps each hub namespace holding addon ManifestWorks
// to the labels that select them.
func addonManifestWorkSelectors(f framework.Framework) map[string]client.MatchingLabels {
	selectors := map[string]client.MatchingLabels{
		f.TestClusterName(): {addonv1beta1.AddonLabelKey: common.AddonName},
	}
	if f.IsHostedMode() {
		selectors[f.HostingClusterName()] = client.MatchingLabels{
			addonv1beta1.AddonLabelKey:          common.AddonName,
			addonv1beta1.AddonNamespaceLabelKey: f.TestClusterName(),
		}
	}
	return selectors
}

func verifyManifestWorksExist(ctx context.Context, f framework.Framework, hubClient client.Client) {
	By("Verifying addon ManifestWorks exist before cleanup")
	for namespace, labels := range addonManifestWorkSelectors(f) {
		works := &workv1.ManifestWorkList{}
		Expect(hubClient.List(ctx, works, client.InNamespace(namespace), labels)).To(Succeed())
		Expect(works.Items).NotTo(BeEmpty(),
			"expected addon ManifestWorks in namespace %s with labels %v", namespace, labels)
	}
}

func expectManifestWorksDeleted(ctx context.Context, f framework.Framework, hubClient client.Client) {
	By("Waiting for addon ManifestWorks to be deleted")
	for namespace, labels := range addonManifestWorkSelectors(f) {
		Eventually(ctx, func() error {
			works := &workv1.ManifestWorkList{}
			if err := hubClient.List(ctx, works, client.InNamespace(namespace), labels); err != nil {
				return err
			}
			if len(works.Items) != 0 {
				return fmt.Errorf("%d ManifestWorks in namespace %s with labels %v still exist",
					len(works.Items), namespace, labels)
			}
			return nil
		}).WithTimeout(cleanupWaitTimeout).WithPolling(cleanupPollInterval).Should(Succeed())
	}
}

func verifyCleanupPreconditions(ctx context.Context, f framework.Framework, installNamespace string) {
	By("Verifying addon agent and managed-cluster identity exist before cleanup")
	Expect(f.AgentRuntimeClient().Get(ctx, types.NamespacedName{
		Namespace: installNamespace,
		Name:      agentDeploymentName,
	}, &appsv1.Deployment{})).To(Succeed())
	Expect(f.SpokeRuntimeClient().Get(ctx, types.NamespacedName{
		Namespace: installNamespace,
		Name:      provisioner.DefaultManagedServiceAccountName,
	}, &corev1.ServiceAccount{})).To(Succeed())
}

func verifyHostedCleanupPreconditions(ctx context.Context, f framework.Framework, installNamespace string) {
	By("Verifying hosted addon placement and ownership before cleanup")
	Expect(installNamespace).To(Equal(f.HostedInstallNamespace()))

	agentClient := f.AgentRuntimeClient()
	spokeClient := f.SpokeRuntimeClient()
	spokeNativeClient := f.SpokeNativeClient()
	Expect(agentClient.Get(ctx, types.NamespacedName{
		Namespace: installNamespace,
		Name:      provisionerDeploymentName,
	}, &appsv1.Deployment{})).To(Succeed())
	Expect(agentClient.Get(ctx, types.NamespacedName{Name: installNamespace}, &corev1.Namespace{})).To(Succeed())
	Expect(spokeClient.Get(ctx, types.NamespacedName{Name: installNamespace}, &corev1.Namespace{})).To(Succeed())

	selector := "addon-agent in (managed-serviceaccount,managed-serviceaccount-kubeconfig-provisioner)"
	spokeDeployments, err := spokeNativeClient.AppsV1().Deployments("").List(
		ctx, metav1.ListOptions{LabelSelector: selector})
	Expect(err).NotTo(HaveOccurred())
	Expect(spokeDeployments.Items).To(BeEmpty(), "hosted addon deployments must not run on the managed cluster")
	spokePods, err := spokeNativeClient.CoreV1().Pods("").List(
		ctx, metav1.ListOptions{LabelSelector: selector})
	Expect(err).NotTo(HaveOccurred())
	Expect(spokePods.Items).To(BeEmpty(), "hosted addon pods must not run on the managed cluster")

	provisionerServiceAccount := &corev1.ServiceAccount{}
	Expect(agentClient.Get(ctx, types.NamespacedName{
		Namespace: installNamespace,
		Name:      provisioner.DefaultHostingServiceAccountName,
	}, provisionerServiceAccount)).To(Succeed())

	managedKubeConfigSecretName := common.AddonName + "-managed-kubeconfig"
	managedKubeConfigSecret := &corev1.Secret{}
	Expect(agentClient.Get(ctx, types.NamespacedName{
		Namespace: installNamespace,
		Name:      managedKubeConfigSecretName,
	}, managedKubeConfigSecret)).To(Succeed())
	Expect(metav1.IsControlledBy(managedKubeConfigSecret, provisionerServiceAccount)).To(BeTrue(),
		"Secret %s/%s must be controlled by ServiceAccount %s",
		installNamespace, managedKubeConfigSecretName, provisionerServiceAccount.Name)
}

func disableAutomaticInstallation(ctx context.Context, hubClient client.Client) {
	By("Disabling automatic addon installation before deletion")
	Eventually(ctx, func() error {
		clusterManagementAddon := &addonv1beta1.ClusterManagementAddOn{}
		if err := hubClient.Get(ctx, types.NamespacedName{Name: common.AddonName}, clusterManagementAddon); err != nil {
			return err
		}
		if clusterManagementAddon.Spec.InstallStrategy.Type == addonv1beta1.AddonInstallStrategyManual &&
			len(clusterManagementAddon.Spec.InstallStrategy.Placements) == 0 {
			return nil
		}
		clusterManagementAddon.Spec.InstallStrategy = addonv1beta1.InstallStrategy{
			Type: addonv1beta1.AddonInstallStrategyManual,
		}
		return hubClient.Update(ctx, clusterManagementAddon)
	}).WithTimeout(cleanupWaitTimeout).WithPolling(cleanupPollInterval).Should(Succeed())
}

func expectAddonResourcesDeleted(ctx context.Context, f framework.Framework, installNamespace string) {
	By("Waiting for addon agent and managed-cluster identity to be deleted")
	expectObjectDeleted(ctx, f.AgentRuntimeClient(), types.NamespacedName{
		Namespace: installNamespace,
		Name:      agentDeploymentName,
	}, &appsv1.Deployment{})
	expectObjectDeleted(ctx, f.SpokeRuntimeClient(), types.NamespacedName{
		Namespace: installNamespace,
		Name:      provisioner.DefaultManagedServiceAccountName,
	}, &corev1.ServiceAccount{})
}

func expectHostedResourcesDeleted(ctx context.Context, f framework.Framework, installNamespace string) {
	By("Waiting for hosted addon namespaces to be deleted")
	expectObjectDeleted(ctx, f.AgentRuntimeClient(), types.NamespacedName{Name: installNamespace}, &corev1.Namespace{})
	expectObjectDeleted(ctx, f.SpokeRuntimeClient(), types.NamespacedName{Name: installNamespace}, &corev1.Namespace{})
}

func expectObjectDeleted(ctx context.Context, c client.Client, key types.NamespacedName, object client.Object) {
	Eventually(ctx, func() error {
		err := c.Get(ctx, key, object)
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		default:
			return fmt.Errorf("%T %s/%s still exists", object, key.Namespace, key.Name)
		}
	}).WithTimeout(cleanupWaitTimeout).WithPolling(cleanupPollInterval).Should(Succeed())
}
