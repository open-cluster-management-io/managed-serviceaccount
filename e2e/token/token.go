package token

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const tokenTestBasename = "token"

var _ = Describe("Token Test",
	func() {
		f := framework.NewE2EFramework(tokenTestBasename)
		targetName := "e2e-" + framework.RunID
		It("Token projection should work", func() {
			msa := &v1alpha1.ManagedServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: f.TestClusterName(),
					Name:      targetName,
				},
				Spec: v1alpha1.ManagedServiceAccountSpec{
					Rotation: v1alpha1.ManagedServiceAccountRotation{
						Enabled:  true,
						Validity: metav1.Duration{Duration: time.Minute * 30},
					},
				},
			}
			err := f.HubRuntimeClient().Create(context.TODO(), msa)
			Expect(err).NotTo(HaveOccurred())

			By("Check local service-account under agent's namespace")
			Eventually(func() (bool, error) {
				addon := &addonv1alpha1.ManagedClusterAddOn{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      common.AddonName,
				}, addon)
				Expect(err).NotTo(HaveOccurred())
				sa := &corev1.ServiceAccount{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: addon.Spec.InstallNamespace,
					Name:      msa.Name,
				}, sa)
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				Expect(err).NotTo(HaveOccurred())
				return true, nil
			}).WithTimeout(30 * time.Second).Should(BeTrue())

			By("Validate the status of ManagedServiceAccount")
			Eventually(func() (bool, error) {
				latest := &v1alpha1.ManagedServiceAccount{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      targetName,
				}, latest)
				Expect(err).NotTo(HaveOccurred())
				if !meta.IsStatusConditionTrue(latest.Status.Conditions, v1alpha1.ConditionTypeSecretCreated) {
					return false, nil
				}
				if !meta.IsStatusConditionTrue(latest.Status.Conditions, v1alpha1.ConditionTypeTokenReported) {
					return false, nil
				}
				if latest.Status.TokenSecretRef == nil {
					return false, nil
				}
				secret := &corev1.Secret{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      latest.Status.TokenSecretRef.Name,
				}, secret)
				Expect(err).NotTo(HaveOccurred())
				return len(secret.Data[corev1.ServiceAccountTokenKey]) > 0, nil
			}).WithTimeout(30 * time.Second).Should(BeTrue())
		})

		It("Token secret deletion should be reconciled", func() {
			latest := &v1alpha1.ManagedServiceAccount{}
			err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
				Namespace: f.TestClusterName(),
				Name:      targetName,
			}, latest)
			Expect(err).NotTo(HaveOccurred())
			err = f.HubNativeClient().CoreV1().
				Secrets(f.TestClusterName()).
				Delete(context.TODO(), latest.Status.TokenSecretRef.Name, metav1.DeleteOptions{})
			Expect(err).NotTo(HaveOccurred())
			By("Secret should re-created after deletion")
			Eventually(func() bool {
				secret := &corev1.Secret{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      latest.Status.TokenSecretRef.Name,
				}, secret)
				if apierrors.IsNotFound(err) {
					return false
				}
				Expect(err).NotTo(HaveOccurred())
				return len(secret.Data[corev1.ServiceAccountTokenKey]) > 0
			}).WithTimeout(30 * time.Second).Should(BeTrue())
		})
	})
