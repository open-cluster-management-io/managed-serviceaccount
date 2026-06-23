package provisioner

import (
	"context"
	"flag"
	"os"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"

	_ "k8s.io/client-go/plugin/pkg/client/auth" //nolint:revive // required for auth plugins
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	managerprovisioner "open-cluster-management.io/managed-serviceaccount/pkg/addon/manager/provisioner"
)

type ManagedKubeconfigProvisionerOptions struct {
	ClusterName string

	SourceNamespace string
	SourceSecret    string
	TargetNamespace string
	TargetSecret    string

	ManagedServiceAccountNamespace string
	ManagedServiceAccountName      string
	TokenExpirationSeconds         int64
	RefreshBefore                  time.Duration
	SyncInterval                   time.Duration
	HubKubeConfigSecret            string

	Cleanup bool
}

func NewManagedKubeconfigProvisioner() *cobra.Command {
	opts := NewManagedKubeconfigProvisionerOptions()

	cmd := &cobra.Command{
		Use:   "managed-kubeconfig-provisioner",
		Short: "Provision a least-privilege managed cluster kubeconfig for hosted mode",
		Run: func(cmd *cobra.Command, args []string) {
			// A canceled context is the normal SIGTERM shutdown path, not a failure.
			if err := opts.Run(ctrl.SetupSignalHandler()); err != nil && !errors.Is(err, context.Canceled) {
				klog.Fatal(err)
			}
		},
	}

	opts.AddFlags(cmd.Flags())
	return cmd
}

func NewManagedKubeconfigProvisionerOptions() *ManagedKubeconfigProvisionerOptions {
	return &ManagedKubeconfigProvisionerOptions{}
}

func (o *ManagedKubeconfigProvisionerOptions) AddFlags(flags *pflag.FlagSet) {
	flags.StringVar(&o.ClusterName, "cluster-name", "", "The managed cluster name.")
	flags.StringVar(&o.SourceNamespace, "source-namespace", "", "The namespace containing the external managed kubeconfig secret. Defaults to --cluster-name.")
	flags.StringVar(&o.SourceSecret, "source-secret", managerprovisioner.DefaultExternalManagedKubeConfigSecret, "The external managed kubeconfig secret name.")
	flags.StringVar(&o.TargetNamespace, "target-namespace", "", "The addon install namespace where the managed kubeconfig secret is stored.")
	flags.StringVar(&o.TargetSecret, "target-secret", "", "The managed kubeconfig secret name generated for the hosted addon agent.")
	flags.StringVar(&o.ManagedServiceAccountNamespace, "managed-serviceaccount-namespace", "", "The managed cluster namespace containing the agent service account. Defaults to --target-namespace.")
	flags.StringVar(&o.ManagedServiceAccountName, "managed-serviceaccount-name", managerprovisioner.DefaultManagedServiceAccountName, "The managed cluster service account used by the agent.")
	flags.StringVar(&o.HubKubeConfigSecret, "hub-kubeconfig-secret", managerprovisioner.DefaultHubKubeConfigSecret, "The hub kubeconfig secret name to copy from the managed cluster into the hosting cluster.")
	flags.Int64Var(&o.TokenExpirationSeconds, "token-expiration-seconds", managerprovisioner.DefaultTokenExpirationSeconds, "Requested TokenRequest expiration seconds.")
	flags.DurationVar(&o.RefreshBefore, "refresh-before", managerprovisioner.DefaultRefreshBefore, "Refresh the generated kubeconfig when the token expires within this duration.")
	flags.DurationVar(&o.SyncInterval, "sync-interval", managerprovisioner.DefaultSyncInterval, "How often to reconcile the generated kubeconfig secret.")
	flags.BoolVar(&o.Cleanup, "cleanup", false, "Delete the generated managed kubeconfig secret and exit.")
}

type reconciler interface {
	Sync(ctx context.Context) error
	Cleanup(ctx context.Context) error
}

const (
	initialErrorBackoff = time.Second
	maxErrorBackoff     = time.Minute
)

func (o *ManagedKubeconfigProvisionerOptions) Run(ctx context.Context) error {
	logger := klog.Background()
	klog.SetOutput(os.Stdout)
	klog.InitFlags(flag.CommandLine)
	ctrl.SetLogger(logger)

	if !o.Cleanup && len(o.ClusterName) == 0 && len(o.SourceNamespace) == 0 {
		return errors.New("--cluster-name or --source-namespace is required")
	}
	if len(o.SourceNamespace) == 0 {
		o.SourceNamespace = o.ClusterName
	}
	if !o.Cleanup && o.SyncInterval <= 0 {
		return errors.Errorf("--sync-interval must be a positive duration, got %s", o.SyncInterval)
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return errors.Wrapf(err, "failed to load hosting cluster in-cluster config")
	}
	hostingClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return errors.Wrapf(err, "failed to build hosting cluster client")
	}

	p := &managerprovisioner.Provisioner{
		HostingClient:                  hostingClient,
		SourceNamespace:                o.SourceNamespace,
		SourceSecret:                   o.SourceSecret,
		TargetNamespace:                o.TargetNamespace,
		TargetSecret:                   o.TargetSecret,
		ManagedServiceAccountNamespace: o.ManagedServiceAccountNamespace,
		ManagedServiceAccountName:      o.ManagedServiceAccountName,
		TokenExpirationSeconds:         o.TokenExpirationSeconds,
		RefreshBefore:                  o.RefreshBefore,
		HubKubeConfigSecret:            o.HubKubeConfigSecret,
	}
	if err := p.Complete(); err != nil {
		return err
	}
	return o.runReconcile(ctx, p)
}

func (o *ManagedKubeconfigProvisionerOptions) runReconcile(ctx context.Context, p reconciler) error {
	if o.Cleanup {
		return p.Cleanup(ctx)
	}

	// Back off exponentially on failure so a transient error doesn't delay the
	// first success by a full --sync-interval.
	errorBackoff := initialErrorBackoff
	for {
		var wait time.Duration
		if err := p.Sync(ctx); err != nil {
			klog.ErrorS(err, "failed to provision managed kubeconfig")
			wait = min(errorBackoff, o.SyncInterval)
			errorBackoff = min(errorBackoff*2, maxErrorBackoff)
		} else {
			errorBackoff = initialErrorBackoff
			wait = o.SyncInterval
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
