package ephemeral_identity

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	authv1alpha1 "open-cluster-management.io/managed-serviceaccount/api/v1alpha1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
)

const testv1alpha1Basename = "ephemeral-v1alpha1"

var _ = Describe("Ephemeral ManagedServiceAccount Test", Label("ephemeral"), func() {
	f := framework.NewE2EFramework(testv1alpha1Basename)
	targetName := "e2e-" + testv1alpha1Basename + "-" + framework.RunID
	var ttlSecond int32 = 2
	ttlDuration := time.Duration(ttlSecond) * time.Second

	It("ManagedServiceAccount ttlAfterCreation should work", func() {
		var err error

		By("Creating an ManagedServiceAccount with TTL")
		msa := &authv1alpha1.ManagedServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: f.TestClusterName(),
				Name:      targetName,
				Finalizers: []string{
					"prevent-resource-removal",
				},
			},
			Spec: authv1alpha1.ManagedServiceAccountSpec{
				Rotation: authv1alpha1.ManagedServiceAccountRotation{
					Enabled:  true,
					Validity: metav1.Duration{Duration: time.Minute * 30},
				},
				TTLSecondsAfterCreation: &ttlSecond,
			},
		}
		err = f.HubRuntimeClient().Create(context.TODO(), msa)
		Expect(err).NotTo(HaveOccurred())

		By("Sleep till TTL to expire")
		time.Sleep(ttlDuration + time.Second)

		By("Checking if ManagedServiceAccount have correct deletion timestamp")
		latest := &authv1alpha1.ManagedServiceAccount{}
		err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
			Namespace: f.TestClusterName(),
			Name:      targetName,
		}, latest)
		Expect(err).NotTo(HaveOccurred())

		createdAt := latest.CreationTimestamp
		deletedAt := latest.DeletionTimestamp
		Expect(deletedAt).ToNot(BeNil())

		lifetime := deletedAt.Time.Sub(createdAt.Time)

		//deletion occured after TTL
		Expect(lifetime >= ttlDuration).To(BeTrue())
		//but not too much after TTL
		Expect(lifetime <= ttlDuration+time.Second).To(BeTrue())

		//remove finalizer
		latest.Finalizers = []string{}
		err = f.HubRuntimeClient().Update(context.TODO(), latest)
		Expect(err).ToNot(HaveOccurred())

		Eventually(func() bool {
			err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
				Namespace: f.TestClusterName(),
				Name:      targetName,
			}, latest)
			if errors.IsNotFound(err) {
				return true
			}
			return false
		}, time.Second*30, time.Second).Should(BeTrue())
	})
})
