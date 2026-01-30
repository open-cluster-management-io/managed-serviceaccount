package features

import (
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2"
)

const (
	// Every feature gate should add method here following this template:
	//
	// // owner: @username
	// // alpha: v1.X
	// MyFeature featuregate.Feature = "MyFeature"

	// owner: @TheRealHaoLiu, @hanqiuzh
	// alpha: v0.1
	//
	// EphemeralIdentity allow user to set TTL on the ManagedServiceAccount resource
	// via spec.ttlSecondsAfterCreation
	EphemeralIdentity featuregate.Feature = "EphemeralIdentity"

	// owner: @morvencao
	// alpha: v0.1
	//
	// ClusterProfileCredSyncer enables the controller that watches ClusterProfile and
	// ManagedServiceAccount resources, syncing token secrets to ClusterProfile namespaces
	ClusterProfileCredSyncer featuregate.Feature = "ClusterProfileCredSyncer"
)

var (
	FeatureGates featuregate.MutableFeatureGate = featuregate.NewFeatureGate()
)

func init() {
	if err := FeatureGates.Add(DefaultManagedServiceAccountFeatureGates); err != nil {
		klog.Fatalf("Unexpected error: %v", err)
	}
}

// DefaultManagedServiceAccountFeatureGates consists of all known ManagedServiceAccount-specific
// feature keys.  To add a new feature, define a key for it above and
// add it here.
var DefaultManagedServiceAccountFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
	EphemeralIdentity:        {Default: false, PreRelease: featuregate.Alpha},
	ClusterProfileCredSyncer: {Default: false, PreRelease: featuregate.Alpha},
}
