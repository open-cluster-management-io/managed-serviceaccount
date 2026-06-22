package provisioner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strconv"
	"time"

	"github.com/pkg/errors"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/clock"
)

const (
	KubeconfigSecretKey = "kubeconfig"
	TokenExpirationKey  = "expirationTimestamp"

	TokenExpirationAnnotation                = "authentication.open-cluster-management.io/token-expiration"
	SourceKubeconfigHashAnnotation           = "authentication.open-cluster-management.io/source-kubeconfig-hash"
	ManagedServiceAccountNamespaceAnnotation = "authentication.open-cluster-management.io/managed-serviceaccount-namespace"
	ManagedServiceAccountNameAnnotation      = "authentication.open-cluster-management.io/managed-serviceaccount-name"
	TokenExpirationSecondsAnnotation         = "authentication.open-cluster-management.io/token-expiration-seconds"

	DefaultExternalManagedKubeConfigSecret = "external-managed-kubeconfig"
	DefaultHubKubeConfigSecret             = "managed-serviceaccount-hub-kubeconfig"
	DefaultManagedServiceAccountName       = "managed-serviceaccount"
	DefaultTokenExpirationSeconds          = int64(3600)
	DefaultRefreshBefore                   = 10 * time.Minute
	DefaultSyncInterval                    = 5 * time.Minute
)

type ManagedClientFactory func(sourceConfig *clientcmdapi.Config) (kubernetes.Interface, error)

type Provisioner struct {
	HostingClient kubernetes.Interface

	SourceNamespace string
	SourceSecret    string
	TargetNamespace string
	TargetSecret    string

	ManagedServiceAccountNamespace string
	ManagedServiceAccountName      string
	TokenExpirationSeconds         int64
	RefreshBefore                  time.Duration
	HubKubeConfigSecret            string

	ManagedClientFactory ManagedClientFactory
	Clock                clock.Clock
}

func NewManagedClient(sourceConfig *clientcmdapi.Config) (kubernetes.Interface, error) {
	cfg, err := clientcmd.NewDefaultClientConfig(*sourceConfig, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to build managed client config from source kubeconfig")
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create managed client from source kubeconfig")
	}
	return client, nil
}

func (p *Provisioner) Sync(ctx context.Context) error {
	source, err := p.HostingClient.CoreV1().Secrets(p.SourceNamespace).Get(ctx, p.SourceSecret, metav1.GetOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to get external managed kubeconfig secret %s/%s", p.SourceNamespace, p.SourceSecret)
	}

	sourceKubeconfig := source.Data[KubeconfigSecretKey]
	if len(sourceKubeconfig) == 0 {
		return errors.Errorf("external managed kubeconfig secret %s/%s missing %q data", p.SourceNamespace, p.SourceSecret, KubeconfigSecretKey)
	}

	sourceConfig, err := clientcmd.Load(sourceKubeconfig)
	if err != nil {
		return errors.Wrapf(err, "failed to load external managed kubeconfig from secret %s/%s", p.SourceNamespace, p.SourceSecret)
	}

	cluster, authInfo, err := currentClusterAndAuthInfo(sourceConfig)
	if err != nil {
		return err
	}
	if err := validateClusterIsPortable(cluster); err != nil {
		return err
	}
	if err := validateAuthInfoIsPortable(authInfo); err != nil {
		return err
	}

	sourceHash, err := sourceKubeconfigHashFromCluster(cluster)
	if err != nil {
		return err
	}

	existing, err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Get(ctx, p.TargetSecret, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		existing = nil
	case err != nil:
		return errors.Wrapf(err, "failed to get managed kubeconfig secret %s/%s", p.TargetNamespace, p.TargetSecret)
	}

	managedClient, err := p.ManagedClientFactory(sourceConfig)
	if err != nil {
		return err
	}
	if err := p.syncHubKubeConfigSecret(ctx, managedClient); err != nil {
		return err
	}
	if p.targetSecretFresh(existing, sourceHash) {
		return nil
	}

	tokenRequest, err := managedClient.CoreV1().ServiceAccounts(p.ManagedServiceAccountNamespace).CreateToken(
		ctx,
		p.ManagedServiceAccountName,
		&authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: &p.TokenExpirationSeconds,
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return errors.Wrapf(err, "failed to request token for managed serviceaccount %s/%s",
			p.ManagedServiceAccountNamespace, p.ManagedServiceAccountName)
	}
	if len(tokenRequest.Status.Token) == 0 {
		return errors.Errorf("token request for managed serviceaccount %s/%s returned an empty token",
			p.ManagedServiceAccountNamespace, p.ManagedServiceAccountName)
	}

	kubeconfig, err := buildManagedKubeconfig(cluster, p.ManagedServiceAccountNamespace, p.ManagedServiceAccountName, tokenRequest.Status.Token)
	if err != nil {
		return err
	}

	expiration := tokenRequest.Status.ExpirationTimestamp.Time.UTC().Format(time.RFC3339)
	desiredAnnotations := map[string]string{
		TokenExpirationAnnotation:                expiration,
		SourceKubeconfigHashAnnotation:           sourceHash,
		ManagedServiceAccountNamespaceAnnotation: p.ManagedServiceAccountNamespace,
		ManagedServiceAccountNameAnnotation:      p.ManagedServiceAccountName,
		TokenExpirationSecondsAnnotation:         strconv.FormatInt(p.TokenExpirationSeconds, 10),
	}
	desiredData := map[string][]byte{
		KubeconfigSecretKey: kubeconfig,
		TokenExpirationKey:  []byte(expiration),
	}

	secret := p.buildTargetSecret(existing, desiredAnnotations, desiredData)
	if existing != nil {
		if _, err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return errors.Wrapf(err, "failed to update managed kubeconfig secret %s/%s", p.TargetNamespace, p.TargetSecret)
		}
	} else {
		if _, err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return errors.Wrapf(err, "failed to create managed kubeconfig secret %s/%s", p.TargetNamespace, p.TargetSecret)
		}
	}
	return nil
}

func (p *Provisioner) syncHubKubeConfigSecret(ctx context.Context, managedClient kubernetes.Interface) error {
	if len(p.HubKubeConfigSecret) == 0 {
		return nil
	}

	source, err := managedClient.CoreV1().Secrets(p.ManagedServiceAccountNamespace).Get(ctx, p.HubKubeConfigSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		existing, existingErr := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Get(ctx, p.HubKubeConfigSecret, metav1.GetOptions{})
		switch {
		case existingErr == nil:
			if len(existing.Data[KubeconfigSecretKey]) == 0 {
				return errors.Errorf("hosted hub kubeconfig secret %s/%s missing %q data",
					p.TargetNamespace, p.HubKubeConfigSecret, KubeconfigSecretKey)
			}
			return nil
		case !apierrors.IsNotFound(existingErr):
			return errors.Wrapf(existingErr, "failed to get hosted hub kubeconfig secret %s/%s",
				p.TargetNamespace, p.HubKubeConfigSecret)
		}
	}
	if err != nil {
		return errors.Wrapf(err, "failed to get managed cluster hub kubeconfig secret %s/%s",
			p.ManagedServiceAccountNamespace, p.HubKubeConfigSecret)
	}
	if len(source.Data[KubeconfigSecretKey]) == 0 {
		return errors.Errorf("managed cluster hub kubeconfig secret %s/%s missing %q data",
			p.ManagedServiceAccountNamespace, p.HubKubeConfigSecret, KubeconfigSecretKey)
	}

	desired := source.DeepCopy()
	desired.ObjectMeta = metav1.ObjectMeta{
		Name:        p.HubKubeConfigSecret,
		Namespace:   p.TargetNamespace,
		Labels:      desired.Labels,
		Annotations: desired.Annotations,
	}

	existing, err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Get(ctx, p.HubKubeConfigSecret, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		existing = nil
	case err != nil:
		return errors.Wrapf(err, "failed to get hosted hub kubeconfig secret %s/%s",
			p.TargetNamespace, p.HubKubeConfigSecret)
	}

	if existing != nil && hubKubeConfigSecretEqual(existing, desired) {
		return nil
	}

	if existing != nil {
		updated := existing.DeepCopy()
		updated.Labels = desired.Labels
		updated.Annotations = desired.Annotations
		updated.Type = desired.Type
		updated.Data = desired.Data
		if _, err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return errors.Wrapf(err, "failed to update hosted hub kubeconfig secret %s/%s",
				p.TargetNamespace, p.HubKubeConfigSecret)
		}
	} else {
		if _, err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return errors.Wrapf(err, "failed to create hosted hub kubeconfig secret %s/%s",
				p.TargetNamespace, p.HubKubeConfigSecret)
		}
	}
	return nil
}

func (p *Provisioner) buildTargetSecret(existing *corev1.Secret, annotations map[string]string, data map[string][]byte) *corev1.Secret {
	var secret *corev1.Secret
	if existing != nil {
		secret = existing.DeepCopy()
	} else {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      p.TargetSecret,
				Namespace: p.TargetNamespace,
			},
		}
	}

	secret.Type = corev1.SecretTypeOpaque
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	for k, v := range annotations {
		secret.Annotations[k] = v
	}
	secret.Data = data
	return secret
}

func (p *Provisioner) Cleanup(ctx context.Context) error {
	if err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Delete(ctx, p.TargetSecret, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return errors.Wrapf(err, "failed to delete managed kubeconfig secret %s/%s", p.TargetNamespace, p.TargetSecret)
	}
	if len(p.HubKubeConfigSecret) != 0 {
		if err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Delete(ctx, p.HubKubeConfigSecret, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return errors.Wrapf(err, "failed to delete hosted hub kubeconfig secret %s/%s", p.TargetNamespace, p.HubKubeConfigSecret)
		}
	}
	return nil
}

// Complete fills in defaults and validates the configuration. It must be called
// before Sync or Cleanup.
func (p *Provisioner) Complete() error {
	if p.HostingClient == nil {
		return errors.New("hosting client is required")
	}
	if len(p.SourceSecret) == 0 {
		p.SourceSecret = DefaultExternalManagedKubeConfigSecret
	}
	if len(p.TargetNamespace) == 0 {
		return errors.New("target namespace is required")
	}
	if len(p.TargetSecret) == 0 {
		return errors.New("managed kubeconfig secret is required")
	}
	if len(p.HubKubeConfigSecret) == 0 {
		p.HubKubeConfigSecret = DefaultHubKubeConfigSecret
	}
	if p.SourceNamespace == p.TargetNamespace && p.SourceSecret == p.TargetSecret {
		return errors.Errorf("managed kubeconfig secret %s/%s must differ from the external managed kubeconfig secret %s/%s",
			p.TargetNamespace, p.TargetSecret, p.SourceNamespace, p.SourceSecret)
	}
	if p.TargetSecret == p.HubKubeConfigSecret {
		return errors.Errorf("managed kubeconfig secret %s/%s must differ from the hosted hub kubeconfig secret %s/%s",
			p.TargetNamespace, p.TargetSecret, p.TargetNamespace, p.HubKubeConfigSecret)
	}
	if p.SourceNamespace == p.TargetNamespace && p.SourceSecret == p.HubKubeConfigSecret {
		return errors.Errorf("external managed kubeconfig secret %s/%s must differ from the hosted hub kubeconfig secret %s/%s",
			p.SourceNamespace, p.SourceSecret, p.TargetNamespace, p.HubKubeConfigSecret)
	}
	if len(p.ManagedServiceAccountNamespace) == 0 {
		p.ManagedServiceAccountNamespace = p.TargetNamespace
	}
	if len(p.ManagedServiceAccountName) == 0 {
		p.ManagedServiceAccountName = DefaultManagedServiceAccountName
	}
	if p.TokenExpirationSeconds == 0 {
		p.TokenExpirationSeconds = DefaultTokenExpirationSeconds
	}
	if p.RefreshBefore == 0 {
		p.RefreshBefore = DefaultRefreshBefore
	}
	if p.TokenExpirationSeconds < 0 {
		return errors.Errorf("token expiration seconds must be positive, got %d", p.TokenExpirationSeconds)
	}
	if p.RefreshBefore < 0 {
		return errors.Errorf("refresh before must be a positive duration, got %s", p.RefreshBefore)
	}
	if tokenLifetime := time.Duration(p.TokenExpirationSeconds) * time.Second; p.RefreshBefore >= tokenLifetime {
		return errors.Errorf("refresh before (%s) must be less than the token lifetime (%s)", p.RefreshBefore, tokenLifetime)
	}
	if p.ManagedClientFactory == nil {
		p.ManagedClientFactory = NewManagedClient
	}
	if p.Clock == nil {
		p.Clock = clock.RealClock{}
	}
	return nil
}

func (p *Provisioner) targetSecretFresh(secret *corev1.Secret, sourceHash string) bool {
	if secret == nil || secret.Annotations == nil {
		return false
	}
	if secret.Annotations[SourceKubeconfigHashAnnotation] != sourceHash ||
		secret.Annotations[ManagedServiceAccountNamespaceAnnotation] != p.ManagedServiceAccountNamespace ||
		secret.Annotations[ManagedServiceAccountNameAnnotation] != p.ManagedServiceAccountName ||
		secret.Annotations[TokenExpirationSecondsAnnotation] != strconv.FormatInt(p.TokenExpirationSeconds, 10) {
		return false
	}
	if len(secret.Data[KubeconfigSecretKey]) == 0 || len(secret.Data[TokenExpirationKey]) == 0 {
		return false
	}

	expiration, err := time.Parse(time.RFC3339, secret.Annotations[TokenExpirationAnnotation])
	if err != nil {
		return false
	}
	return expiration.After(p.Clock.Now().UTC().Add(p.RefreshBefore))
}

func hubKubeConfigSecretEqual(a, b *corev1.Secret) bool {
	return a.Type == b.Type &&
		reflect.DeepEqual(a.Labels, b.Labels) &&
		reflect.DeepEqual(a.Annotations, b.Annotations) &&
		reflect.DeepEqual(a.Data, b.Data)
}

func buildManagedKubeconfig(cluster *clientcmdapi.Cluster, namespace, serviceAccountName, token string) ([]byte, error) {
	config := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"managed": cluster.DeepCopy(),
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			serviceAccountName: {
				Token: token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"managed": {
				Cluster:   "managed",
				AuthInfo:  serviceAccountName,
				Namespace: namespace,
			},
		},
		CurrentContext: "managed",
	}

	kubeconfig, err := clientcmd.Write(config)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to write managed serviceaccount kubeconfig")
	}
	return kubeconfig, nil
}

func currentClusterAndAuthInfo(config *clientcmdapi.Config) (*clientcmdapi.Cluster, *clientcmdapi.AuthInfo, error) {
	if config == nil {
		return nil, nil, errors.New("external managed kubeconfig is empty")
	}
	contextName := config.CurrentContext
	if len(contextName) == 0 && len(config.Contexts) == 1 {
		for name := range config.Contexts {
			contextName = name
		}
	}
	context := config.Contexts[contextName]
	if context == nil {
		return nil, nil, errors.Errorf("external managed kubeconfig current context %q not found", contextName)
	}
	cluster := config.Clusters[context.Cluster]
	if cluster == nil {
		return nil, nil, errors.Errorf("external managed kubeconfig cluster %q not found", context.Cluster)
	}
	authInfo := config.AuthInfos[context.AuthInfo]
	if authInfo == nil {
		return nil, nil, errors.Errorf("external managed kubeconfig user %q not found", context.AuthInfo)
	}
	return cluster, authInfo, nil
}

func validateClusterIsPortable(cluster *clientcmdapi.Cluster) error {
	if len(cluster.CertificateAuthority) > 0 {
		return errors.Errorf("external managed kubeconfig cluster references certificate-authority file %q, which is not portable",
			cluster.CertificateAuthority)
	}
	return nil
}

func validateAuthInfoIsPortable(authInfo *clientcmdapi.AuthInfo) error {
	if authInfo.Exec != nil {
		return errors.Errorf("external managed kubeconfig user references exec credential plugin %q, which is not portable",
			authInfo.Exec.Command)
	}
	for _, ref := range []struct{ path, field string }{
		{authInfo.ClientCertificate, "client-certificate"},
		{authInfo.ClientKey, "client-key"},
		{authInfo.TokenFile, "token"},
	} {
		if len(ref.path) > 0 {
			return errors.Errorf("external managed kubeconfig user references %s file %q, which is not portable",
				ref.field, ref.path)
		}
	}
	return nil
}

// sourceKubeconfigHashFromCluster hashes the canonical cluster form so formatting-only changes don't churn the token.
func sourceKubeconfigHashFromCluster(cluster *clientcmdapi.Cluster) (string, error) {
	sanitized := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"managed": cluster.DeepCopy(),
		},
		Contexts: map[string]*clientcmdapi.Context{
			"managed": {
				Cluster: "managed",
			},
		},
		CurrentContext: "managed",
	}
	data, err := clientcmd.Write(sanitized)
	if err != nil {
		return "", errors.Wrapf(err, "failed to write external managed kubeconfig hash input")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
