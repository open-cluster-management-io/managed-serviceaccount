package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

func TestUniqueAgentInstallNamespace(t *testing.T) {
	testScheme := runtime.NewScheme()
	assert.NoError(t, addonv1beta1.Install(testScheme))

	const configuredNamespaceAnnotation = "test.open-cluster-management.io/install-namespace"
	const resolveFailureAnnotation = "test.open-cluster-management.io/resolve-failure"
	resolve := agent.AgentInstallNamespaceFunc(func(_ context.Context, addon *addonv1beta1.ManagedClusterAddOn) (string, error) {
		if message, ok := addon.Annotations[resolveFailureAnnotation]; ok {
			return "", errors.New(message)
		}
		return addon.Annotations[configuredNamespaceAnnotation], nil
	})

	newAddon := func(clusterName, hostingClusterName, installNamespace string) *addonv1beta1.ManagedClusterAddOn {
		addon := newTestAddOn(common.AddonName, clusterName)
		addon.Annotations = map[string]string{}
		if len(hostingClusterName) > 0 {
			addon.Annotations[addonv1beta1.HostingClusterNameAnnotationKey] = hostingClusterName
		}
		if len(installNamespace) > 0 {
			addon.Annotations[configuredNamespaceAnnotation] = installNamespace
		}
		return addon
	}

	withInstallNamespaceAnnotation := func(addon *addonv1beta1.ManagedClusterAddOn, namespace string) *addonv1beta1.ManagedClusterAddOn {
		addon.Annotations[addonv1beta1.InstallNamespaceAnnotation] = namespace
		return addon
	}

	baseTime := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	createdAt := func(addon *addonv1beta1.ManagedClusterAddOn, offset time.Duration) *addonv1beta1.ManagedClusterAddOn {
		addon.CreationTimestamp = metav1.NewTime(baseTime.Add(offset))
		return addon
	}
	withUID := func(addon *addonv1beta1.ManagedClusterAddOn, uid string) *addonv1beta1.ManagedClusterAddOn {
		addon.UID = types.UID(uid)
		return addon
	}
	withResolveFailure := func(addon *addonv1beta1.ManagedClusterAddOn, message string) *addonv1beta1.ManagedClusterAddOn {
		addon.Annotations[resolveFailureAnnotation] = message
		return addon
	}
	withStatusNamespace := func(addon *addonv1beta1.ManagedClusterAddOn, namespace string) *addonv1beta1.ManagedClusterAddOn {
		addon.Status.Namespace = namespace
		return addon
	}

	cases := []struct {
		name              string
		current           *addonv1beta1.ManagedClusterAddOn
		others            []*addonv1beta1.ManagedClusterAddOn
		inputStatus       *string
		omitCurrent       bool
		expectedNamespace string
		expectedError     string
	}{
		{
			name:              "non-hosted addon preserves the configured namespace",
			current:           newAddon("cluster1", "", "shared"),
			expectedNamespace: "shared",
		},
		{
			name:              "unique hosted namespace",
			current:           newAddon("cluster1", "hosting1", "cluster1-addon"),
			others:            []*addonv1beta1.ManagedClusterAddOn{newAddon("cluster2", "hosting1", "cluster2-addon")},
			expectedNamespace: "cluster1-addon",
		},
		{
			name:              "same namespace on another hosting cluster",
			current:           newAddon("cluster1", "hosting1", "shared"),
			others:            []*addonv1beta1.ManagedClusterAddOn{newAddon("cluster2", "hosting2", "shared")},
			expectedNamespace: "shared",
		},
		{
			name:          "duplicate explicit hosted namespace rejects the newer addon",
			current:       createdAt(newAddon("cluster1", "hosting1", "shared"), time.Hour),
			others:        []*addonv1beta1.ManagedClusterAddOn{createdAt(newAddon("cluster2", "hosting1", "shared"), 0)},
			expectedError: `addon cluster1/managed-serviceaccount cannot use install namespace "shared" on agent cluster "hosting1": already used by cluster2/managed-serviceaccount`,
		},
		{
			name:          "hosted addon requires a dedicated namespace",
			current:       newAddon("cluster1", "hosting1", ""),
			expectedError: `hosted addon cluster1/managed-serviceaccount must use a dedicated install namespace on agent cluster "hosting1" instead of "` + addonfactory.AddonDefaultInstallNamespace + `"`,
		},
		{
			name:              "older addon keeps a contested namespace",
			current:           createdAt(newAddon("cluster1", "hosting1", "shared"), 0),
			others:            []*addonv1beta1.ManagedClusterAddOn{createdAt(newAddon("cluster2", "hosting1", "shared"), time.Hour)},
			expectedNamespace: "shared",
		},
		{
			name:          "uid breaks a creation timestamp tie",
			current:       withUID(createdAt(newAddon("cluster1", "hosting1", "shared"), 0), "uid-b"),
			others:        []*addonv1beta1.ManagedClusterAddOn{withUID(createdAt(newAddon("cluster2", "hosting1", "shared"), 0), "uid-a")},
			expectedError: `addon cluster1/managed-serviceaccount cannot use install namespace "shared" on agent cluster "hosting1": already used by cluster2/managed-serviceaccount`,
		},
		{
			name:              "a running incumbent keeps a namespace claimed by an older addon",
			current:           createdAt(withStatusNamespace(newAddon("cluster1", "hosting1", "shared"), "shared"), time.Hour),
			others:            []*addonv1beta1.ManagedClusterAddOn{createdAt(withStatusNamespace(newAddon("cluster2", "hosting1", "shared"), "cluster2-old"), 0)},
			expectedNamespace: "shared",
		},
		{
			name:              "registration input uses the persisted self namespace claim",
			current:           createdAt(withStatusNamespace(newAddon("cluster1", "hosting1", "shared"), "shared"), time.Hour),
			others:            []*addonv1beta1.ManagedClusterAddOn{createdAt(withStatusNamespace(newAddon("cluster2", "hosting1", "shared"), "cluster2-old"), 0)},
			inputStatus:       ptr.To(addonfactory.AddonDefaultInstallNamespace),
			expectedNamespace: "shared",
		},
		{
			name:          "an older addon reconfigured onto an occupied namespace is rejected",
			current:       createdAt(withStatusNamespace(newAddon("cluster1", "hosting1", "shared"), "cluster1-old"), 0),
			others:        []*addonv1beta1.ManagedClusterAddOn{createdAt(withStatusNamespace(newAddon("cluster2", "hosting1", "shared"), "shared"), time.Hour)},
			expectedError: `addon cluster1/managed-serviceaccount cannot use install namespace "shared" on agent cluster "hosting1": already used by cluster2/managed-serviceaccount`,
		},
		{
			name:    "a peer moving away still defends its current namespace",
			current: createdAt(newAddon("cluster1", "hosting1", "shared"), 0),
			others: []*addonv1beta1.ManagedClusterAddOn{
				createdAt(withStatusNamespace(newAddon("cluster2", "hosting1", "cluster2-new"), "shared"), time.Hour),
			},
			expectedError: `addon cluster1/managed-serviceaccount cannot use install namespace "shared" on agent cluster "hosting1": already used by cluster2/managed-serviceaccount`,
		},
		{
			name:              "skips undeployed peers that fail to resolve",
			current:           newAddon("cluster1", "hosting1", "cluster1-addon"),
			others:            []*addonv1beta1.ManagedClusterAddOn{withResolveFailure(newAddon("cluster2", "hosting1", "cluster1-addon"), "deployment config not found")},
			expectedNamespace: "cluster1-addon",
		},
		{
			name:    "a deployed peer that fails to resolve still defends its namespace",
			current: createdAt(newAddon("cluster1", "hosting1", "cluster2-addon"), time.Hour),
			others: []*addonv1beta1.ManagedClusterAddOn{
				withResolveFailure(withStatusNamespace(createdAt(newAddon("cluster2", "hosting1", ""), 0), "cluster2-addon"), "deployment config not found"),
			},
			expectedError: `addon cluster1/managed-serviceaccount cannot use install namespace "cluster2-addon" on agent cluster "hosting1": already used by cluster2/managed-serviceaccount`,
		},
		{
			name:              "install namespace annotation only collides with the same annotation",
			current:           withInstallNamespaceAnnotation(newAddon("cluster1", "hosting1", ""), "annotated"),
			others:            []*addonv1beta1.ManagedClusterAddOn{newAddon("cluster2", "hosting1", "")},
			expectedNamespace: "",
		},
		{
			name:          "duplicate hosted namespace from the install namespace annotation",
			current:       createdAt(withInstallNamespaceAnnotation(newAddon("cluster1", "hosting1", ""), "annotated"), time.Hour),
			others:        []*addonv1beta1.ManagedClusterAddOn{createdAt(withInstallNamespaceAnnotation(newAddon("cluster2", "hosting1", ""), "annotated"), 0)},
			expectedError: `addon cluster1/managed-serviceaccount cannot use install namespace "annotated" on agent cluster "hosting1": already used by cluster2/managed-serviceaccount`,
		},
		{
			name:          "hosted addon cannot explicitly select the default namespace",
			current:       withInstallNamespaceAnnotation(newAddon("cluster1", "hosting1", ""), addonfactory.AddonDefaultInstallNamespace),
			expectedError: `hosted addon cluster1/managed-serviceaccount must use a dedicated install namespace on agent cluster "hosting1" instead of "` + addonfactory.AddonDefaultInstallNamespace + `"`,
		},
		{
			name:              "default addon ignores an invalid undeployed hosted peer",
			current:           createdAt(newAddon("hosting1", "", ""), time.Hour),
			others:            []*addonv1beta1.ManagedClusterAddOn{createdAt(newAddon("cluster1", "hosting1", ""), 0)},
			expectedNamespace: "",
		},
		{
			name:          "default addon preserves a deployed hosted peer claim",
			current:       createdAt(newAddon("hosting1", "", ""), time.Hour),
			others:        []*addonv1beta1.ManagedClusterAddOn{createdAt(withStatusNamespace(newAddon("cluster1", "hosting1", ""), addonfactory.AddonDefaultInstallNamespace), 0)},
			expectedError: `addon hosting1/managed-serviceaccount cannot use install namespace "` + addonfactory.AddonDefaultInstallNamespace + `" on agent cluster "hosting1": already used by cluster1/managed-serviceaccount`,
		},
		{
			name:          "fails closed while the persisted self is absent from the cache",
			current:       newAddon("cluster1", "hosting1", "shared"),
			omitCurrent:   true,
			expectedError: `failed to validate addon install namespace "shared" for cluster1/managed-serviceaccount: failed to read persisted addon: managedclusteraddons.addon.open-cluster-management.io "managed-serviceaccount" not found`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			objects := make([]client.Object, 0, len(c.others)+1)
			cachedAddons := append([]*addonv1beta1.ManagedClusterAddOn(nil), c.others...)
			if !c.omitCurrent {
				cachedAddons = append(cachedAddons, c.current)
			}
			for _, addon := range cachedAddons {
				objects = append(objects, addon)
			}
			addonReader := fakeclient.NewClientBuilder().
				WithScheme(testScheme).
				WithIndex(&addonv1beta1.ManagedClusterAddOn{}, addonPlacementIndexName, indexAddonPlacement).
				WithObjects(objects...).
				Build()
			resolveUnique := uniqueAgentInstallNamespace(addonReader, resolve)
			input := c.current.DeepCopy()
			if c.inputStatus != nil {
				input.Status.Namespace = *c.inputStatus
			}

			namespace, err := resolveUnique(context.Background(), input)

			if len(c.expectedError) > 0 {
				assert.EqualError(t, err, c.expectedError)
				assert.Empty(t, namespace)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, c.expectedNamespace, namespace)
		})
	}
}
