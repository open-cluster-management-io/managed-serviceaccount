package token

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const tokenv1alpha1TestBasename = "token-v1alpha1"

var _ = Describe("Token Test",
	func() {
		f := framework.NewE2EFramework(tokenv1alpha1TestBasename)
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

			By("Validate the validitiy of the generated token", func() {
				validateToken(f, targetName)
			})
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

			By("Re-validate the re-generated token", func() {
				validateToken(f, targetName)
			})
		})

		It("ManagedServiceAccount deletion should delete ServiceAccount", func() {
			var err error

			addon := &addonv1alpha1.ManagedClusterAddOn{}
			err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
				Namespace: f.TestClusterName(),
				Name:      common.AddonName,
			}, addon)
			Expect(err).NotTo(HaveOccurred())

			msa := &v1alpha1.ManagedServiceAccount{}
			err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
				Namespace: f.TestClusterName(),
				Name:      targetName,
			}, msa)
			Expect(err).NotTo(HaveOccurred())

			secret := &corev1.Secret{}
			err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
				Namespace: f.TestClusterName(),
				Name:      msa.Status.TokenSecretRef.Name,
			}, secret)
			Expect(err).NotTo(HaveOccurred())

			serviceAccount := &corev1.ServiceAccount{}
			err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
				Namespace: addon.Spec.InstallNamespace,
				Name:      targetName,
			}, serviceAccount)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting ManagedServiceAccount", func() {
				err = f.HubRuntimeClient().Delete(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())
			})

			//managed serviceaccount should be deleted
			Eventually(func() bool {
				msa := &v1alpha1.ManagedServiceAccount{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      targetName,
				}, msa)
				if errors.IsNotFound(err) {
					return true
				}
				return false
			}, time.Minute, time.Second).Should(BeTrue())

			//token secret should be deleted
			Eventually(func() bool {
				secret := &corev1.Secret{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: f.TestClusterName(),
					Name:      msa.Status.TokenSecretRef.Name,
				}, secret)
				if errors.IsNotFound(err) {
					return true
				}
				return false
			}, time.Minute, time.Second).Should(BeTrue())

			//serviceaccount should be deleted
			Eventually(func() bool {
				serviceAccount := &corev1.ServiceAccount{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: addon.Spec.InstallNamespace,
					Name:      targetName,
				}, serviceAccount)
				if errors.IsNotFound(err) {
					return true
				}
				return false
			}, time.Minute, time.Second).Should(BeTrue())
		})
	},
)
