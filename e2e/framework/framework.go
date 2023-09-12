package framework

import (
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// unique identifier of the e2e run
var RunID = rand.String(6)

type Framework interface {
	HubRESTConfig() *rest.Config
	TestClusterName() string

	HubNativeClient() kubernetes.Interface
	HubRuntimeClient() client.Client
}

var _ Framework = &framework{}

type framework struct {
	basename string
	ctx      *E2EContext
}

func NewE2EFramework(basename string) Framework {
	f := &framework{
		basename: basename,
		ctx:      e2eContext,
	}
	BeforeEach(f.BeforeEach)
	AfterEach(f.AfterEach)
	return f
}

func (f *framework) HubRESTConfig() *rest.Config {
	restConfig, err := clientcmd.BuildConfigFromFlags("", f.ctx.HubKubeConfig)
	Expect(err).NotTo(HaveOccurred())
	return restConfig
}

func (f *framework) HubNativeClient() kubernetes.Interface {
	cfg := f.HubRESTConfig()
	nativeClient, err := kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	return nativeClient
}

func (f *framework) HubRuntimeClient() client.Client {
	cfg := f.HubRESTConfig()
	runtimeClient, err := client.New(cfg, client.Options{
		Scheme: scheme,
	})
	Expect(err).NotTo(HaveOccurred())
	return runtimeClient
}

func (f *framework) TestClusterName() string {
	return f.ctx.TestCluster
}

func (f *framework) BeforeEach() {
	logger := klogr.New()
	ctrl.SetLogger(logger)
}

func (f *framework) AfterEach() {
}
