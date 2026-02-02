package clusterprofile_creds

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientauthenticationv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	"open-cluster-management.io/managed-serviceaccount/pkg/addon/manager/controller"
	cpv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
)

const (
	clusterProfileCredsBasename = "clusterprofile-creds"
	pollInterval                = 2 * time.Second
	pollTimeout                 = 60 * time.Second
)

var _ = Describe("ClusterProfile Credentials Sync and Plugin Test", Label("clusterprofile"),
	func() {
		f := framework.NewE2EFramework(clusterProfileCredsBasename)
		targetClusterName := f.TestClusterName() // This will be used as both namespace and ClusterProfile name
		msaName1 := "e2e-msa-" + framework.RunID + "-1"
		msaName2 := "e2e-msa-" + framework.RunID + "-2"

		Context("ClusterProfileCredSyncer Controller", func() {
			var clusterProfile *cpv1alpha1.ClusterProfile
			var clusterProfileNamespace string
			var testCounter int

			BeforeEach(func() {
				// Create a unique namespace for this test's ClusterProfile (unique per test iteration)
				testCounter++
				clusterProfileNamespace = fmt.Sprintf("cp-test-%s-%d", framework.RunID, testCounter)
				By("Creating namespace for ClusterProfile: " + clusterProfileNamespace)
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: clusterProfileNamespace,
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), ns)
				Expect(err).NotTo(HaveOccurred())

				By("Creating ClusterProfile in dynamic test namespace")
				clusterProfile = &cpv1alpha1.ClusterProfile{
					ObjectMeta: metav1.ObjectMeta{
						Name:      targetClusterName,
						Namespace: clusterProfileNamespace,
						Labels: map[string]string{
							cpv1alpha1.LabelClusterManagerKey: controller.ClusterProfileManagerName,
							clusterv1.ClusterNameLabelKey:     targetClusterName,
						},
					},
					Spec: cpv1alpha1.ClusterProfileSpec{
						DisplayName: targetClusterName,
						ClusterManager: cpv1alpha1.ClusterManager{
							Name: controller.ClusterProfileManagerName,
						},
					},
				}
				err = f.HubRuntimeClient().Create(context.TODO(), clusterProfile)
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				By("Cleaning up ClusterProfile")
				if clusterProfile != nil {
					err := f.HubRuntimeClient().Delete(context.TODO(), clusterProfile)
					if err != nil && !apierrors.IsNotFound(err) {
						Expect(err).NotTo(HaveOccurred())
					}

					// Wait for ClusterProfile to be deleted
					Eventually(func() bool {
						cp := &cpv1alpha1.ClusterProfile{}
						err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
							Namespace: clusterProfileNamespace,
							Name:      targetClusterName,
						}, cp)
						return apierrors.IsNotFound(err)
					}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())
				}
				By("Cleaning up ClusterProfile namespace")
				if clusterProfileNamespace != "" {
					ns := &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: clusterProfileNamespace,
						},
					}
					err := f.HubRuntimeClient().Delete(context.TODO(), ns)
					if err != nil && !apierrors.IsNotFound(err) {
						Expect(err).NotTo(HaveOccurred())
					}

					// Wait for namespace to be fully deleted
					Eventually(func() bool {
						ns := &corev1.Namespace{}
						err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
							Name: clusterProfileNamespace,
						}, ns)
						return apierrors.IsNotFound(err)
					}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())
				}
			})

			It("Should sync ManagedServiceAccount token secrets to ClusterProfile namespace", func() {
				By("Creating ManagedServiceAccount in cluster namespace")
				msa1 := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: targetClusterName,
						Name:      msaName1,
						Labels: map[string]string{
							controller.LabelKeyClusterProfileSync: "true",
						},
					},
					Spec: authv1beta1.ManagedServiceAccountSpec{
						Rotation: authv1beta1.ManagedServiceAccountRotation{
							Validity: metav1.Duration{Duration: time.Minute * 30},
						},
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), msa1)
				Expect(err).NotTo(HaveOccurred())

				By("Waiting for ManagedServiceAccount token secret to be created")
				var tokenSecretName string
				Eventually(func() bool {
					latest := &authv1beta1.ManagedServiceAccount{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: targetClusterName,
						Name:      msaName1,
					}, latest)
					if err != nil {
						return false
					}
					if !meta.IsStatusConditionTrue(latest.Status.Conditions, authv1beta1.ConditionTypeSecretCreated) {
						return false
					}
					if latest.Status.TokenSecretRef == nil {
						return false
					}
					tokenSecretName = latest.Status.TokenSecretRef.Name

					// Verify source secret exists with token
					secret := &corev1.Secret{}
					err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: targetClusterName,
						Name:      tokenSecretName,
					}, secret)
					if err != nil {
						return false
					}
					return len(secret.Data[corev1.ServiceAccountTokenKey]) > 0
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Verifying synced credential secret exists in ClusterProfile namespace")
				syncedSecretName := fmt.Sprintf("%s-%s", targetClusterName, msaName1)
				Eventually(func() bool {
					syncedSecret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, syncedSecret)
					if err != nil {
						return false
					}

					// Verify secret has correct label
					expectedSyncedFrom := fmt.Sprintf("%s-%s", targetClusterName, msaName1)
					if syncedSecret.Labels[controller.LabelKeySyncedFrom] != expectedSyncedFrom {
						return false
					}

					// Verify secret has token data
					if len(syncedSecret.Data[corev1.ServiceAccountTokenKey]) == 0 {
						return false
					}

					// Verify owner reference is set
					if len(syncedSecret.OwnerReferences) != 1 {
						return false
					}
					if syncedSecret.OwnerReferences[0].Name != targetClusterName {
						return false
					}
					if syncedSecret.OwnerReferences[0].Kind != "ClusterProfile" {
						return false
					}
					if syncedSecret.OwnerReferences[0].Controller == nil || !*syncedSecret.OwnerReferences[0].Controller {
						return false
					}

					return true
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Cleaning up ManagedServiceAccount")
				err = f.HubRuntimeClient().Delete(context.TODO(), msa1)
				Expect(err).NotTo(HaveOccurred())
			})

			It("Should sync multiple ManagedServiceAccounts from the same namespace", func() {
				By("Creating two ManagedServiceAccounts")
				msa1 := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: targetClusterName,
						Name:      msaName1,
						Labels: map[string]string{
							controller.LabelKeyClusterProfileSync: "true",
						},
					},
					Spec: authv1beta1.ManagedServiceAccountSpec{
						Rotation: authv1beta1.ManagedServiceAccountRotation{
							Validity: metav1.Duration{Duration: time.Minute * 30},
						},
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), msa1)
				Expect(err).NotTo(HaveOccurred())

				msa2 := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: targetClusterName,
						Name:      msaName2,
						Labels: map[string]string{
							controller.LabelKeyClusterProfileSync: "true",
						},
					},
					Spec: authv1beta1.ManagedServiceAccountSpec{
						Rotation: authv1beta1.ManagedServiceAccountRotation{
							Validity: metav1.Duration{Duration: time.Minute * 30},
						},
					},
				}
				err = f.HubRuntimeClient().Create(context.TODO(), msa2)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying both synced secrets exist")
				syncedSecretName1 := fmt.Sprintf("%s-%s", targetClusterName, msaName1)
				syncedSecretName2 := fmt.Sprintf("%s-%s", targetClusterName, msaName2)

				Eventually(func() bool {
					secret1 := &corev1.Secret{}
					err1 := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName1,
					}, secret1)

					secret2 := &corev1.Secret{}
					err2 := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName2,
					}, secret2)

					return err1 == nil && err2 == nil &&
						len(secret1.Data[corev1.ServiceAccountTokenKey]) > 0 &&
						len(secret2.Data[corev1.ServiceAccountTokenKey]) > 0
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Cleaning up ManagedServiceAccounts")
				err = f.HubRuntimeClient().Delete(context.TODO(), msa1)
				Expect(err).NotTo(HaveOccurred())
				err = f.HubRuntimeClient().Delete(context.TODO(), msa2)
				Expect(err).NotTo(HaveOccurred())
			})

			It("Should cleanup orphaned synced secrets when ManagedServiceAccount is deleted", func() {
				By("Creating ManagedServiceAccount")
				msa := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: targetClusterName,
						Name:      msaName1,
						Labels: map[string]string{
							controller.LabelKeyClusterProfileSync: "true",
						},
					},
					Spec: authv1beta1.ManagedServiceAccountSpec{
						Rotation: authv1beta1.ManagedServiceAccountRotation{
							Validity: metav1.Duration{Duration: time.Minute * 30},
						},
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())

				By("Waiting for synced secret to be created")
				syncedSecretName := fmt.Sprintf("%s-%s", targetClusterName, msaName1)
				Eventually(func() bool {
					secret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, secret)
					return err == nil && len(secret.Data[corev1.ServiceAccountTokenKey]) > 0
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Deleting ManagedServiceAccount")
				err = f.HubRuntimeClient().Delete(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying synced secret is cleaned up")
				Eventually(func() bool {
					secret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, secret)
					return apierrors.IsNotFound(err)
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())
			})

			It("Should cleanup all synced secrets when ClusterProfile is deleted via owner references", func() {
				By("Creating ManagedServiceAccount")
				msa := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: targetClusterName,
						Name:      msaName1,
						Labels: map[string]string{
							controller.LabelKeyClusterProfileSync: "true",
						},
					},
					Spec: authv1beta1.ManagedServiceAccountSpec{
						Rotation: authv1beta1.ManagedServiceAccountRotation{
							Validity: metav1.Duration{Duration: time.Minute * 30},
						},
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())

				By("Waiting for synced secret to be created")
				syncedSecretName := fmt.Sprintf("%s-%s", targetClusterName, msaName1)
				Eventually(func() bool {
					secret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, secret)
					return err == nil && len(secret.Data[corev1.ServiceAccountTokenKey]) > 0
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Deleting ClusterProfile")
				err = f.HubRuntimeClient().Delete(context.TODO(), clusterProfile)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying synced secret is cleaned up by garbage collection")
				Eventually(func() bool {
					secret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, secret)
					return apierrors.IsNotFound(err)
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Cleaning up ManagedServiceAccount")
				err = f.HubRuntimeClient().Delete(context.TODO(), msa)
				if err != nil && !apierrors.IsNotFound(err) {
					Expect(err).NotTo(HaveOccurred())
				}

				// Set to nil so AfterEach doesn't try to delete again
				clusterProfile = nil
			})

			It("Should update synced secret when ManagedServiceAccount token changes", func() {
				By("Creating ManagedServiceAccount")
				msa := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: targetClusterName,
						Name:      msaName1,
						Labels: map[string]string{
							controller.LabelKeyClusterProfileSync: "true",
						},
					},
					Spec: authv1beta1.ManagedServiceAccountSpec{
						Rotation: authv1beta1.ManagedServiceAccountRotation{
							Validity: metav1.Duration{Duration: time.Minute * 30},
						},
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())

				By("Waiting for synced secret to be created")
				syncedSecretName := fmt.Sprintf("%s-%s", targetClusterName, msaName1)
				var originalToken []byte
				Eventually(func() bool {
					secret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, secret)
					if err == nil && len(secret.Data[corev1.ServiceAccountTokenKey]) > 0 {
						originalToken = secret.Data[corev1.ServiceAccountTokenKey]
						return true
					}
					return false
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Deleting the source token secret to trigger rotation")
				latest := &authv1beta1.ManagedServiceAccount{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: targetClusterName,
					Name:      msaName1,
				}, latest)
				Expect(err).NotTo(HaveOccurred())
				Expect(latest.Status.TokenSecretRef).NotTo(BeNil())

				sourceSecret := &corev1.Secret{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: targetClusterName,
					Name:      latest.Status.TokenSecretRef.Name,
				}, sourceSecret)
				Expect(err).NotTo(HaveOccurred())

				err = f.HubRuntimeClient().Delete(context.TODO(), sourceSecret)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying synced secret is updated with new token")
				Eventually(func() bool {
					secret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, secret)
					if err != nil {
						return false
					}
					newToken := secret.Data[corev1.ServiceAccountTokenKey]
					// Token should be different after rotation
					return len(newToken) > 0 && string(newToken) != string(originalToken)
				}).WithTimeout(pollTimeout * 2).WithPolling(pollInterval).Should(BeTrue())

				By("Cleaning up ManagedServiceAccount")
				err = f.HubRuntimeClient().Delete(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())
			})

			It("Should detect and sync token secret updates via watch", func() {
				By("Creating ManagedServiceAccount")
				msa := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: targetClusterName,
						Name:      msaName1,
						Labels: map[string]string{
							controller.LabelKeyClusterProfileSync: "true",
						},
					},
					Spec: authv1beta1.ManagedServiceAccountSpec{
						Rotation: authv1beta1.ManagedServiceAccountRotation{
							Validity: metav1.Duration{Duration: time.Minute * 30},
						},
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())

				By("Waiting for initial token secret and synced credential to be created")
				syncedSecretName := fmt.Sprintf("%s-%s", targetClusterName, msaName1)
				var tokenSecretName string
				var originalToken []byte
				Eventually(func() bool {
					latest := &authv1beta1.ManagedServiceAccount{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: targetClusterName,
						Name:      msaName1,
					}, latest)
					if err != nil || latest.Status.TokenSecretRef == nil {
						return false
					}
					tokenSecretName = latest.Status.TokenSecretRef.Name

					// Verify synced secret exists with token
					syncedSecret := &corev1.Secret{}
					err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, syncedSecret)
					if err == nil && len(syncedSecret.Data[corev1.ServiceAccountTokenKey]) > 0 {
						originalToken = syncedSecret.Data[corev1.ServiceAccountTokenKey]
						return true
					}
					return false
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Directly updating the token secret to simulate rotation")
				tokenSecret := &corev1.Secret{}
				err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
					Namespace: targetClusterName,
					Name:      tokenSecretName,
				}, tokenSecret)
				Expect(err).NotTo(HaveOccurred())

				// Update the token data to a new value
				newTokenValue := []byte("rotated-token-" + framework.RunID)
				tokenSecret.Data[corev1.ServiceAccountTokenKey] = newTokenValue
				err = f.HubRuntimeClient().Update(context.TODO(), tokenSecret)
				Expect(err).NotTo(HaveOccurred())

				By("Verifying synced credential is automatically updated via token secret watch")
				Eventually(func() bool {
					syncedSecret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, syncedSecret)
					if err != nil {
						return false
					}

					// Verify the synced secret now has the new token value
					syncedToken := syncedSecret.Data[corev1.ServiceAccountTokenKey]
					return len(syncedToken) > 0 &&
						string(syncedToken) == string(newTokenValue) &&
						string(syncedToken) != string(originalToken)
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue(),
					"Synced credential should be updated when token secret changes")

				By("Cleaning up ManagedServiceAccount")
				err = f.HubRuntimeClient().Delete(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())
			})

			It("Should handle multiple token secret updates in quick succession", func() {
				By("Creating ManagedServiceAccount")
				msa := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: targetClusterName,
						Name:      msaName1,
						Labels: map[string]string{
							controller.LabelKeyClusterProfileSync: "true",
						},
					},
					Spec: authv1beta1.ManagedServiceAccountSpec{
						Rotation: authv1beta1.ManagedServiceAccountRotation{
							Validity: metav1.Duration{Duration: time.Minute * 30},
						},
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())

				By("Waiting for initial setup")
				syncedSecretName := fmt.Sprintf("%s-%s", targetClusterName, msaName1)
				var tokenSecretName string
				Eventually(func() bool {
					latest := &authv1beta1.ManagedServiceAccount{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: targetClusterName,
						Name:      msaName1,
					}, latest)
					if err != nil || latest.Status.TokenSecretRef == nil {
						return false
					}
					tokenSecretName = latest.Status.TokenSecretRef.Name

					syncedSecret := &corev1.Secret{}
					err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, syncedSecret)
					return err == nil && len(syncedSecret.Data[corev1.ServiceAccountTokenKey]) > 0
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Performing multiple rapid token updates")
				finalTokenValue := []byte("final-token-" + framework.RunID)
				for i := 0; i < 3; i++ {
					tokenSecret := &corev1.Secret{}
					err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: targetClusterName,
						Name:      tokenSecretName,
					}, tokenSecret)
					Expect(err).NotTo(HaveOccurred())

					if i < 2 {
						tokenSecret.Data[corev1.ServiceAccountTokenKey] = []byte(fmt.Sprintf("intermediate-token-%d-%s", i, framework.RunID))
					} else {
						tokenSecret.Data[corev1.ServiceAccountTokenKey] = finalTokenValue
					}
					err = f.HubRuntimeClient().Update(context.TODO(), tokenSecret)
					Expect(err).NotTo(HaveOccurred())

					// Small delay between updates
					time.Sleep(100 * time.Millisecond)
				}

				By("Verifying synced credential eventually has the final token value")
				Eventually(func() bool {
					syncedSecret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, syncedSecret)
					if err != nil {
						return false
					}
					return string(syncedSecret.Data[corev1.ServiceAccountTokenKey]) == string(finalTokenValue)
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue(),
					"Synced credential should eventually reflect the final token value")

				By("Cleaning up ManagedServiceAccount")
				err = f.HubRuntimeClient().Delete(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("ClusterProfile Credentials Plugin", func() {
			var clusterProfile *cpv1alpha1.ClusterProfile
			var pluginPath string
			var clusterProfileNamespace string
			var testCounter int

			BeforeEach(func() {
				// Create a unique namespace for this test's ClusterProfile (unique per test iteration)
				testCounter++
				clusterProfileNamespace = fmt.Sprintf("cp-plugin-test-%s-%d", framework.RunID, testCounter)
				By("Creating namespace for ClusterProfile: " + clusterProfileNamespace)
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: clusterProfileNamespace,
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), ns)
				Expect(err).NotTo(HaveOccurred())

				By("Building the credentials plugin binary")
				tmpDir := GinkgoT().TempDir()
				pluginPath = filepath.Join(tmpDir, "cp-creds")
				cmd := exec.Command("go", "build", "-o", pluginPath, "./cmd/clusterprofile-credentials-plugin")
				output, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), "Failed to build plugin: %s", string(output))

				By("Creating ClusterProfile")
				clusterProfile = &cpv1alpha1.ClusterProfile{
					ObjectMeta: metav1.ObjectMeta{
						Name:      targetClusterName,
						Namespace: clusterProfileNamespace,
						Labels: map[string]string{
							cpv1alpha1.LabelClusterManagerKey: controller.ClusterProfileManagerName,
							clusterv1.ClusterNameLabelKey:     targetClusterName,
						},
					},
					Spec: cpv1alpha1.ClusterProfileSpec{
						DisplayName: targetClusterName,
						ClusterManager: cpv1alpha1.ClusterManager{
							Name: controller.ClusterProfileManagerName,
						},
					},
				}
				err = f.HubRuntimeClient().Create(context.TODO(), clusterProfile)
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				By("Cleaning up ClusterProfile")
				if clusterProfile != nil {
					err := f.HubRuntimeClient().Delete(context.TODO(), clusterProfile)
					if err != nil && !apierrors.IsNotFound(err) {
						Expect(err).NotTo(HaveOccurred())
					}
				}
				By("Cleaning up ClusterProfile namespace")
				if clusterProfileNamespace != "" {
					ns := &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: clusterProfileNamespace,
						},
					}
					err := f.HubRuntimeClient().Delete(context.TODO(), ns)
					if err != nil && !apierrors.IsNotFound(err) {
						Expect(err).NotTo(HaveOccurred())
					}

					// Wait for namespace to be fully deleted
					Eventually(func() bool {
						ns := &corev1.Namespace{}
						err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
							Name: clusterProfileNamespace,
						}, ns)
						return apierrors.IsNotFound(err)
					}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())
				}
			})

			It("Should retrieve token from synced credential secret", func() {
				By("Creating ManagedServiceAccount")
				msa := &authv1beta1.ManagedServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: targetClusterName,
						Name:      msaName1,
						Labels: map[string]string{
							controller.LabelKeyClusterProfileSync: "true",
						},
					},
					Spec: authv1beta1.ManagedServiceAccountSpec{
						Rotation: authv1beta1.ManagedServiceAccountRotation{
							Validity: metav1.Duration{Duration: time.Minute * 30},
						},
					},
				}
				err := f.HubRuntimeClient().Create(context.TODO(), msa)
				Expect(err).NotTo(HaveOccurred())
				defer func() {
					_ = f.HubRuntimeClient().Delete(context.TODO(), msa)
				}()

				By("Waiting for synced secret to be created")
				syncedSecretName := fmt.Sprintf("%s-%s", targetClusterName, msaName1)
				var expectedToken string
				Eventually(func() bool {
					secret := &corev1.Secret{}
					err := f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
						Namespace: clusterProfileNamespace,
						Name:      syncedSecretName,
					}, secret)
					if err == nil && len(secret.Data[corev1.ServiceAccountTokenKey]) > 0 {
						expectedToken = string(secret.Data[corev1.ServiceAccountTokenKey])
						return true
					}
					return false
				}).WithTimeout(pollTimeout).WithPolling(pollInterval).Should(BeTrue())

				By("Testing plugin retrieves correct token")
				// Create ExecCredential input
				clusterConfigJSON := fmt.Sprintf(`{"clusterName":"%s"}`, targetClusterName)
				execCred := clientauthenticationv1.ExecCredential{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "client.authentication.k8s.io/v1",
						Kind:       "ExecCredential",
					},
					Spec: clientauthenticationv1.ExecCredentialSpec{
						Cluster: &clientauthenticationv1.Cluster{
							Server: "https://test-server:6443",
							Config: runtime.RawExtension{
								Raw: []byte(clusterConfigJSON),
							},
						},
					},
				}

				execCredJSON, err := json.Marshal(execCred)
				Expect(err).NotTo(HaveOccurred())

				// Run the plugin
				cmd := exec.Command(pluginPath, "--managed-serviceaccount="+msaName1)
				cmd.Stdin = nil
				cmd.Env = append(os.Environ(),
					"NAMESPACE="+clusterProfileNamespace,
					"KUBERNETES_EXEC_INFO="+string(execCredJSON),
				)

				output, err := cmd.CombinedOutput()
				Expect(err).NotTo(HaveOccurred(), "Plugin execution failed: %s", string(output))

				// Parse the output
				var result clientauthenticationv1.ExecCredential
				err = json.Unmarshal(output, &result)
				Expect(err).NotTo(HaveOccurred(), "Failed to parse plugin output: %s", string(output))

				// Verify the token matches
				Expect(result.Status.Token).To(Equal(expectedToken), "Plugin should return the correct token")
			})

			It("Should return error when synced secret does not exist", func() {
				By("Testing plugin with non-existent ManagedServiceAccount")
				clusterConfigJSON := fmt.Sprintf(`{"clusterName":"%s"}`, targetClusterName)
				execCred := clientauthenticationv1.ExecCredential{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "client.authentication.k8s.io/v1",
						Kind:       "ExecCredential",
					},
					Spec: clientauthenticationv1.ExecCredentialSpec{
						Cluster: &clientauthenticationv1.Cluster{
							Server: "https://test-server:6443",
							Config: runtime.RawExtension{
								Raw: []byte(clusterConfigJSON),
							},
						},
					},
				}

				execCredJSON, err := json.Marshal(execCred)
				Expect(err).NotTo(HaveOccurred())

				cmd := exec.Command(pluginPath, "--managed-serviceaccount=nonexistent")
				cmd.Stdin = nil
				cmd.Env = append(os.Environ(),
					"KUBERNETES_EXEC_INFO="+string(execCredJSON),
				)

				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Plugin should fail when secret doesn't exist")
				Expect(string(output)).To(ContainSubstring("failed to get synced credential secret"))
			})

			It("Should return error when required flag is missing", func() {
				By("Testing plugin without --managed-serviceaccount flag")
				cmd := exec.Command(pluginPath)
				output, err := cmd.CombinedOutput()
				Expect(err).To(HaveOccurred(), "Plugin should fail when required flag is missing")
				Expect(string(output)).To(ContainSubstring("--managed-serviceaccount flag is required"))
			})
		})
	})
