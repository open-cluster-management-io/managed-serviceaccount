package e2e

import (
	"testing"

	"github.com/onsi/ginkgo/v2"
)

func RunE2ETests(t *testing.T) {
	ginkgo.RunSpecs(t, "ManagedServiceAccount e2e suite")
}
