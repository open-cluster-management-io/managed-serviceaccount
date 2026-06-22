package manager

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	addonconstants "open-cluster-management.io/addon-framework/pkg/addonmanager/constants"
	"open-cluster-management.io/addon-framework/pkg/agent"
	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
)

const addonPlacementIndexName = "managed-serviceaccount-agent-cluster"

// SetupAgentInstallNamespaceResolver indexes addons by the cluster where their agent
// runs and returns an install namespace resolver that rejects a namespace already
// claimed by another instance of the same addon on that cluster.
func SetupAgentInstallNamespaceResolver(
	ctx context.Context,
	addonCache cache.Cache,
	resolve agent.AgentInstallNamespaceFunc,
) (agent.AgentInstallNamespaceFunc, error) {
	if err := addonCache.IndexField(
		ctx,
		&addonv1beta1.ManagedClusterAddOn{},
		addonPlacementIndexName,
		indexAddonPlacement,
	); err != nil {
		return nil, fmt.Errorf("failed to index managed cluster addon placement: %w", err)
	}
	return uniqueAgentInstallNamespace(addonCache, resolve), nil
}

func uniqueAgentInstallNamespace(
	addonReader client.Reader,
	resolve agent.AgentInstallNamespaceFunc,
) agent.AgentInstallNamespaceFunc {
	return func(ctx context.Context, addon *addonv1beta1.ManagedClusterAddOn) (string, error) {
		installNamespace, err := resolve(ctx, addon)
		if err != nil {
			return "", err
		}

		effectiveInstallNamespace := effectiveAgentInstallNamespace(addon, installNamespace)
		if err := validateAgentInstallNamespace(addon, effectiveInstallNamespace); err != nil {
			return "", err
		}

		// The addon-framework registration controller replaces Status.Namespace
		// on the object copy passed to this resolver. Read the persisted object so
		// an existing placement remains authoritative during registration.
		persistedAddon := &addonv1beta1.ManagedClusterAddOn{}
		if err := addonReader.Get(ctx, client.ObjectKeyFromObject(addon), persistedAddon); err != nil {
			return "", fmt.Errorf(
				"failed to validate addon install namespace %q for %s/%s: failed to read persisted addon: %w",
				effectiveInstallNamespace, addon.Namespace, addon.Name, err,
			)
		}

		agentClusterName := addonAgentCluster(addon)
		addons := &addonv1beta1.ManagedClusterAddOnList{}
		if err := addonReader.List(ctx, addons, client.MatchingFields{
			addonPlacementIndexName: addonPlacementIndexKey(agentClusterName, addon.Name),
		}); err != nil {
			return "", fmt.Errorf("failed to validate addon install namespace %q: %w", effectiveInstallNamespace, err)
		}

		for i := range addons.Items {
			other := &addons.Items[i]
			if other.Namespace == addon.Namespace && other.Name == addon.Name {
				continue
			}

			// status.namespace is the peer's current placement. Keep treating it
			// as a claim even when a new configuration resolves elsewhere and the
			// old hosting resources have not been removed yet.
			otherClaimsNamespace := other.Status.Namespace == effectiveInstallNamespace
			otherInstallNamespace, err := resolve(ctx, other)
			if err != nil {
				if len(other.Status.Namespace) == 0 {
					// An undeployed peer that cannot resolve fails its own
					// reconcile and reports its own status; do not let it
					// block this addon.
					klog.Warningf("skipping addon %s/%s while validating install namespace %q for %s/%s: %v",
						other.Namespace, other.Name, effectiveInstallNamespace, addon.Namespace, addon.Name, err)
				}
			} else {
				otherEffectiveNamespace := effectiveAgentInstallNamespace(other, otherInstallNamespace)
				if err := validateAgentInstallNamespace(other, otherEffectiveNamespace); err != nil {
					// Preserve an invalid peer's current status claim, but do not let
					// an undeployed desired placement reserve the Default namespace.
					klog.V(4).InfoS("Ignoring invalid desired addon install namespace",
						"addonNamespace", other.Namespace, "addonName", other.Name, "err", err)
				} else if otherEffectiveNamespace == effectiveInstallNamespace {
					otherClaimsNamespace = true
				}
			}
			if !otherClaimsNamespace {
				continue
			}
			if claimPrecedes(persistedAddon, other, effectiveInstallNamespace) {
				continue
			}
			return "", fmt.Errorf(
				"addon %s/%s cannot use install namespace %q on agent cluster %q: already used by %s/%s",
				addon.Namespace, addon.Name, effectiveInstallNamespace, agentClusterName, other.Namespace, other.Name,
			)
		}

		return installNamespace, nil
	}
}

// claimPrecedes reports whether addon claims the contested install namespace
// ahead of other. An addon already holding the namespace in status.namespace
// beats one that merely wants it, so reconfiguring another addon onto a live
// claim is rejected; when neither or both hold it, the older addon wins, and
// UID breaks ties deterministically. Distinct persisted Kubernetes objects
// cannot have the same UID.
func claimPrecedes(addon, other *addonv1beta1.ManagedClusterAddOn, namespace string) bool {
	addonOccupies := addon.Status.Namespace == namespace
	if otherOccupies := other.Status.Namespace == namespace; addonOccupies != otherOccupies {
		return addonOccupies
	}
	if !addon.CreationTimestamp.Equal(&other.CreationTimestamp) {
		return addon.CreationTimestamp.Before(&other.CreationTimestamp)
	}
	return addon.UID < other.UID
}

func indexAddonPlacement(obj client.Object) []string {
	addon, ok := obj.(*addonv1beta1.ManagedClusterAddOn)
	if !ok {
		return nil
	}
	return []string{addonPlacementIndexKey(addonAgentCluster(addon), addon.Name)}
}

func addonPlacementIndexKey(agentClusterName, addonName string) string {
	return agentClusterName + "/" + addonName
}

func addonAgentCluster(addon *addonv1beta1.ManagedClusterAddOn) string {
	installMode, hostingClusterName := addonconstants.GetHostedModeInfo(addon, nil)
	if installMode == addonconstants.InstallModeHosted {
		return hostingClusterName
	}
	return addon.Namespace
}

func validateAgentInstallNamespace(addon *addonv1beta1.ManagedClusterAddOn, namespace string) error {
	installMode, _ := addonconstants.GetHostedModeInfo(addon, nil)
	if installMode == addonconstants.InstallModeHosted && namespace == addonfactory.AddonDefaultInstallNamespace {
		return fmt.Errorf(
			"hosted addon %s/%s must use a dedicated install namespace on agent cluster %q instead of %q",
			addon.Namespace, addon.Name, addonAgentCluster(addon), addonfactory.AddonDefaultInstallNamespace,
		)
	}
	return nil
}

// effectiveAgentInstallNamespace mirrors how the addon-framework resolves the agent
// install namespace when rendering, so uniqueness is checked against the namespace
// the agent actually lands in.
func effectiveAgentInstallNamespace(addon *addonv1beta1.ManagedClusterAddOn, configuredNamespace string) string {
	if len(configuredNamespace) > 0 {
		return configuredNamespace
	}
	if installNamespace := addon.Annotations[addonv1beta1.InstallNamespaceAnnotation]; len(installNamespace) > 0 {
		return installNamespace
	}
	return addonfactory.AddonDefaultInstallNamespace
}
