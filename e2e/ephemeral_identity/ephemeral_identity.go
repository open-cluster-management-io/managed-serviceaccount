package ephemeral_identity

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	authv1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
)

const testBasename = "ephemeral"

var _ = Describe("Ephemeral ManagedServiceAccount Test", Label("ephemeral"), func() {
	f := framework.NewE2EFramework(testBasename)
	targetName := "e2e-" + testBasename + "-" + framework.RunID
	var ttlSecond int32 = 2
	ttlDuration := time.Duration(ttlSecond) * time.Second

	It("ManagedServiceAccount ttlAfterCreation should work", func() {
		var err error

		By("Creating an ManagedServiceAccount with TTL")
		msa := &authv1beta1.ManagedServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: f.TestClusterName(),
				Name:      targetName,
				Finalizers: []string{
					"prevent-resource-removal",
				},
			},
			Spec: authv1beta1.ManagedServiceAccountSpec{
				Rotation: authv1beta1.ManagedServiceAccountRotation{
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
		latest := &authv1beta1.ManagedServiceAccount{}
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

		Eventually(func() error {
			err = f.HubRuntimeClient().Get(context.TODO(), types.NamespacedName{
				Namespace: f.TestClusterName(),
				Name:      targetName,
			}, latest)
			if err == nil {
				return fmt.Errorf("managed serviceaccount %s/%s still exists", f.TestClusterName(), targetName)
			}
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}, time.Second*30, time.Second).ShouldNot(HaveOccurred())
	})
})
