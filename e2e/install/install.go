package install

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const installTestBasename = "install"

var _ = Describe("Addon Installation Test",
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
	})
