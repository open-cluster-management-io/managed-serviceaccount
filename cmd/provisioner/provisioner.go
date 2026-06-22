package provisioner

import (
	"context"
	"os"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth" //nolint:revive // required for auth plugins
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"open-cluster-management.io/managed-serviceaccount/pkg/addon/provisioner"
	"open-cluster-management.io/sdk-go/pkg/basecontroller/events"
)

type ProvisionerOptions struct {
	SourceNamespace string
	SourceSecret    string
	TargetNamespace string
	TargetSecret    string

	ManagedServiceAccountNamespace string
	ManagedServiceAccountName      string
	HostingServiceAccountName      string
	TokenExpirationSeconds         int64
	RefreshBefore                  time.Duration
	SyncInterval                   time.Duration
}

func NewProvisioner() *cobra.Command {
	opts := NewProvisionerOptions()

	cmd := &cobra.Command{
		Use:   "managed-kubeconfig-provisioner",
		Short: "Provision a least-privilege managed cluster kubeconfig for an agent running on a hosting cluster",
		Run: func(cmd *cobra.Command, args []string) {
			if err := opts.Run(ctrl.SetupSignalHandler()); err != nil {
				klog.Fatal(err)
			}
		},
	}

	opts.AddFlags(cmd.Flags())
	return cmd
}

func NewProvisionerOptions() *ProvisionerOptions {
	return &ProvisionerOptions{}
}

func (o *ProvisionerOptions) AddFlags(flags *pflag.FlagSet) {
	flags.StringVar(&o.SourceNamespace, "source-namespace", "", "The namespace containing the external managed kubeconfig secret.")
	flags.StringVar(&o.SourceSecret, "source-secret", provisioner.DefaultExternalManagedKubeConfigSecret, "The external managed kubeconfig secret name.")
	flags.StringVar(&o.TargetNamespace, "target-namespace", "", "The addon install namespace where the managed kubeconfig secret is stored.")
	flags.StringVar(&o.TargetSecret, "target-secret", "", "The managed kubeconfig secret name generated for the hosted addon agent.")
	flags.StringVar(&o.ManagedServiceAccountNamespace, "managed-serviceaccount-namespace", "", "The managed cluster namespace containing the agent service account. Defaults to --target-namespace.")
	flags.StringVar(&o.ManagedServiceAccountName, "managed-serviceaccount-name", provisioner.DefaultManagedServiceAccountName, "The managed cluster service account used by the agent.")
	flags.StringVar(&o.HostingServiceAccountName, "hosting-service-account-name", provisioner.DefaultHostingServiceAccountName, "The hosting cluster service account that owns generated secrets.")
	flags.Int64Var(&o.TokenExpirationSeconds, "token-expiration-seconds", provisioner.DefaultTokenExpirationSeconds, "Requested TokenRequest expiration seconds.")
	flags.DurationVar(&o.RefreshBefore, "refresh-before", provisioner.DefaultRefreshBefore, "Refresh the generated kubeconfig when the token expires within this duration.")
	flags.DurationVar(&o.SyncInterval, "sync-interval", provisioner.DefaultSyncInterval, "Maximum interval between generated kubeconfig reconciles; token expiration may trigger an earlier reconcile.")
}

func (o *ProvisionerOptions) Run(ctx context.Context) error {
	logger := klog.Background()
	klog.SetOutput(os.Stdout)
	ctrl.SetLogger(logger)

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return errors.Wrapf(err, "failed to load hosting cluster in-cluster config")
	}
	cfg.Timeout = provisioner.DefaultRequestTimeout
	hostingClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return errors.Wrapf(err, "failed to build hosting cluster client")
	}
	eventRecorder, err := events.NewEventRecorder(
		ctx,
		clientgoscheme.Scheme,
		hostingClient.EventsV1(),
		"managed-kubeconfig-provisioner",
	)
	if err != nil {
		return errors.Wrapf(err, "failed to create event recorder")
	}

	p := &provisioner.Provisioner{
		HostingClient:                  hostingClient,
		EventRecorder:                  eventRecorder,
		SourceNamespace:                o.SourceNamespace,
		SourceSecret:                   o.SourceSecret,
		TargetNamespace:                o.TargetNamespace,
		TargetSecret:                   o.TargetSecret,
		ManagedServiceAccountNamespace: o.ManagedServiceAccountNamespace,
		ManagedServiceAccountName:      o.ManagedServiceAccountName,
		HostingServiceAccountName:      o.HostingServiceAccountName,
		TokenExpirationSeconds:         o.TokenExpirationSeconds,
		RefreshBefore:                  o.RefreshBefore,
		SyncInterval:                   o.SyncInterval,
	}
	if err := p.Complete(); err != nil {
		return err
	}
	return p.Start(ctx)
}
