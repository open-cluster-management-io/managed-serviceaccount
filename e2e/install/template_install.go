package install

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"

	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const templateInstallTestBasename = "template-install"

var _ = Describe("Template Addon Installation Test", Label("install"), Label("template-install"),
	func() {
		f := framework.NewE2EFramework(templateInstallTestBasename)
		defaultNs := "open-cluster-management-agent-addon"
		It("Ignore managed cluster addon spec install namespace", func() {
			By("Update managed cluster addon spec install namespace")
			Eventually(func() error {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      common.AddonName,
				}, addon)
				Expect(err).NotTo(HaveOccurred())

				addonCopy := addon.DeepCopy()
				addonCopy.Spec.InstallNamespace = "test-ns"
				return f.HubRuntimeClient().Update(context.Background(), addonCopy)
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Make sure addon is available")
			Eventually(func() error {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      common.AddonName,
				}, addon)
				Expect(err).NotTo(HaveOccurred())

				if !meta.IsStatusConditionTrue(addon.Status.Conditions, addonv1alpha1.ManagedClusterAddOnConditionAvailable) {
					return fmt.Errorf("addon is unavailable")
				}

				if addon.Status.Namespace != defaultNs {
					return fmt.Errorf("addon is not installed in default namespace")
				}
				return nil
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())

			By("Make sure addon is installed in default namespace")
			Eventually(func() error {
				_, err := f.HubNativeClient().AppsV1().Deployments(defaultNs).Get(
					context.Background(), "managed-serviceaccount-addon-agent", metav1.GetOptions{})
				return err
			}).WithTimeout(time.Minute).ShouldNot(HaveOccurred())
		})

	})
