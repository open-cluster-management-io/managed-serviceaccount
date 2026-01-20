package e2e

import (
	"os"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"open-cluster-management.io/managed-serviceaccount/e2e/framework"
	// per-package e2e suite

	//_ "open-cluster-management.io/cluster-proxy/e2e/configuration"
	_ "open-cluster-management.io/managed-serviceaccount/e2e/clusterprofile_creds"
	_ "open-cluster-management.io/managed-serviceaccount/e2e/ephemeral_identity"
	_ "open-cluster-management.io/managed-serviceaccount/e2e/install"
	_ "open-cluster-management.io/managed-serviceaccount/e2e/token"
)

func TestMain(m *testing.M) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	framework.ParseFlags()
	os.Exit(m.Run())
}

func TestE2E(t *testing.T) {
	RunE2ETests(t)
}
