package framework

import (
	. "github.com/onsi/ginkgo/v2" //nolint:revive,staticcheck // idiomatic ginkgo usage
	. "github.com/onsi/gomega"    //nolint:revive,staticcheck // idiomatic gomega usage

	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// unique identifier of the e2e run
var RunID = rand.String(6)

type Framework interface {
	HubRESTConfig() *rest.Config
	SpokeRESTConfig() *rest.Config
	AgentRESTConfig() *rest.Config
	HubKubeConfigPath() string
	SpokeKubeConfigPath() string
	TestClusterName() string
	IsHostedMode() bool
	ExternalManagedKubeConfigNamespace() string
	ExternalManagedKubeConfigSecret() string
	HostingClusterName() string
	HostedInstallNamespace() string

	HubNativeClient() kubernetes.Interface
	HubRuntimeClient() client.Client
	SpokeNativeClient() kubernetes.Interface
	SpokeRuntimeClient() client.Client
	AgentNativeClient() kubernetes.Interface
	AgentRuntimeClient() client.Client
}

var _ Framework = &framework{}

type framework struct {
	basename string
	ctx      *E2EContext
}

func newFramework(basename string) *framework {
	return &framework{
		basename: basename,
		ctx:      e2eContext,
	}
}

func NewE2EFramework(basename string) Framework {
	f := newFramework(basename)
	BeforeEach(f.BeforeEach)
	return f
}

// NewSuiteFramework builds a Framework for suite-level setup nodes such as
// BeforeSuite, where per-spec BeforeEach/AfterEach registration is not wanted.
func NewSuiteFramework(basename string) Framework {
	return newFramework(basename)
}

func (f *framework) HubRESTConfig() *rest.Config {
	return f.restConfig(f.ctx.HubKubeConfig)
}

func (f *framework) SpokeRESTConfig() *rest.Config {
	return f.restConfig(f.ctx.SpokeKubeConfig)
}

func (f *framework) AgentRESTConfig() *rest.Config {
	return f.restConfig(f.ctx.AgentKubeConfig)
}

func (f *framework) restConfig(kubeconfig string) *rest.Config {
	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())
	return restConfig
}

func (f *framework) HubNativeClient() kubernetes.Interface {
	return f.nativeClient(f.HubRESTConfig())
}

func (f *framework) HubRuntimeClient() client.Client {
	return f.runtimeClient(f.HubRESTConfig())
}

func (f *framework) SpokeNativeClient() kubernetes.Interface {
	return f.nativeClient(f.SpokeRESTConfig())
}

func (f *framework) SpokeRuntimeClient() client.Client {
	return f.runtimeClient(f.SpokeRESTConfig())
}

func (f *framework) AgentNativeClient() kubernetes.Interface {
	return f.nativeClient(f.AgentRESTConfig())
}

func (f *framework) AgentRuntimeClient() client.Client {
	return f.runtimeClient(f.AgentRESTConfig())
}

func (f *framework) nativeClient(cfg *rest.Config) kubernetes.Interface {
	nativeClient, err := kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	return nativeClient
}

func (f *framework) runtimeClient(cfg *rest.Config) client.Client {
	runtimeClient, err := client.New(cfg, client.Options{
		Scheme: scheme,
	})
	Expect(err).NotTo(HaveOccurred())
	return runtimeClient
}

func (f *framework) TestClusterName() string {
	return f.ctx.TestCluster
}

func (f *framework) HubKubeConfigPath() string {
	return f.ctx.HubKubeConfig
}

func (f *framework) SpokeKubeConfigPath() string {
	return f.ctx.SpokeKubeConfig
}

func (f *framework) IsHostedMode() bool {
	return len(f.ctx.HostingClusterName) != 0
}

func (f *framework) ExternalManagedKubeConfigNamespace() string {
	return f.ctx.ExternalManagedKubeConfigNamespace
}

func (f *framework) ExternalManagedKubeConfigSecret() string {
	return f.ctx.ExternalManagedKubeConfigSecret
}

func (f *framework) HostingClusterName() string {
	return f.ctx.HostingClusterName
}

func (f *framework) HostedInstallNamespace() string {
	return f.ctx.HostedInstallNamespace
}

func (f *framework) BeforeEach() {
	logger := klogr.New() //nolint:staticcheck // textlogger not vendored, klogr works fine for e2e tests
	ctrl.SetLogger(logger)
}
