package cleanup

import (
	"os"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
)

func TestMain(m *testing.M) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	framework.ParseFlags()
	os.Exit(m.Run())
}

func TestE2E(t *testing.T) {
	ginkgo.RunSpecs(t, "ManagedServiceAccount e2e suite -- cleanup tests")
}
