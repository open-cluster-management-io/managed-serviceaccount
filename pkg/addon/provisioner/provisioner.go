package provisioner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
)

const (
	KubeconfigSecretKey                = "kubeconfig"
	ManagedServiceAccountUIDAnnotation = "authentication.open-cluster-management.io/managed-serviceaccount-uid"

	DefaultExternalManagedKubeConfigSecret = "external-managed-kubeconfig"
	DefaultManagedServiceAccountName       = "managed-serviceaccount"
	DefaultHostingServiceAccountName       = "managed-serviceaccount-kubeconfig-provisioner"
	DefaultTokenExpirationSeconds          = int64(3600)
	DefaultRefreshBefore                   = 10 * time.Minute
	DefaultSyncInterval                    = 5 * time.Minute
	DefaultRequestTimeout                  = 30 * time.Second
	minTokenExpirationSeconds              = int64(10 * 60)
	maxTokenExpirationSeconds              = int64(1 << 32)
)

const (
	tokenExpirationKey   = "expirationTimestamp"
	managedTokenFilePath = "/etc/managed/token"

	tokenExpirationAnnotation                = "authentication.open-cluster-management.io/token-expiration"
	sourceKubeconfigHashAnnotation           = "authentication.open-cluster-management.io/source-kubeconfig-hash"
	managedServiceAccountNamespaceAnnotation = "authentication.open-cluster-management.io/managed-serviceaccount-namespace"
	managedServiceAccountNameAnnotation      = "authentication.open-cluster-management.io/managed-serviceaccount-name"
	tokenExpirationSecondsAnnotation         = "authentication.open-cluster-management.io/token-expiration-seconds"

	initialErrorBackoff = time.Second
	maxErrorBackoff     = time.Minute
)

// Result describes when the provisioner must next reconcile to refresh a
// generated token before it expires.
type Result struct {
	RequeueAfter time.Duration
}

type ManagedClientFactory func(sourceConfig *clientcmdapi.Config) (kubernetes.Interface, error)

type Provisioner struct {
	HostingClient kubernetes.Interface

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

	ManagedClientFactory ManagedClientFactory
	Clock                clock.Clock
}

// ValidateRotationSettings validates the token lifetime and reconcile timing
// shared by the addon renderer and the provisioner process.
func ValidateRotationSettings(tokenExpirationSeconds int64, refreshBefore, syncInterval time.Duration) error {
	if tokenExpirationSeconds < minTokenExpirationSeconds || tokenExpirationSeconds > maxTokenExpirationSeconds {
		return errors.Errorf("token expiration seconds must be between %d and %d, got %d",
			minTokenExpirationSeconds, maxTokenExpirationSeconds, tokenExpirationSeconds)
	}
	if refreshBefore <= 0 {
		return errors.Errorf("refresh before must be a positive duration, got %s", refreshBefore)
	}
	if syncInterval <= 0 {
		return errors.Errorf("sync interval must be a positive duration, got %s", syncInterval)
	}

	tokenLifetime := time.Duration(tokenExpirationSeconds) * time.Second
	if refreshBefore >= tokenLifetime {
		return errors.Errorf("refresh before (%s) must be less than the token lifetime (%s)", refreshBefore, tokenLifetime)
	}
	return nil
}

func newManagedClient(sourceConfig *clientcmdapi.Config) (kubernetes.Interface, error) {
	cfg, err := clientcmd.NewDefaultClientConfig(*sourceConfig, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to build managed client config from source kubeconfig")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultRequestTimeout
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create managed client from source kubeconfig")
	}
	return client, nil
}

// Start reconciles until ctx is done. Complete must be called first.
func (p *Provisioner) Start(ctx context.Context) error {
	return runReconcile(ctx, p.Sync, p.SyncInterval)
}

func runReconcile(ctx context.Context, sync func(context.Context) (Result, error), syncInterval time.Duration) error {
	// Back off exponentially on failure so a transient error doesn't delay the
	// first success by a full sync interval.
	errorBackoff := initialErrorBackoff
	for {
		var wait time.Duration
		result, err := sync(ctx)
		if err != nil {
			klog.ErrorS(err, "failed to provision managed kubeconfig")
			wait = min(errorBackoff, syncInterval)
			errorBackoff = min(errorBackoff*2, maxErrorBackoff)
		} else {
			errorBackoff = initialErrorBackoff
			wait = syncInterval
			if result.RequeueAfter > 0 {
				wait = min(wait, result.RequeueAfter)
			}
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (p *Provisioner) Sync(ctx context.Context) (Result, error) {
	sourceConfig, cluster, sourceHash, err := p.loadSourceKubeconfig(ctx)
	if err != nil {
		return Result{}, err
	}

	hostingServiceAccount, err := p.HostingClient.CoreV1().ServiceAccounts(p.TargetNamespace).Get(
		ctx,
		p.HostingServiceAccountName,
		metav1.GetOptions{},
	)
	if err != nil {
		return Result{}, errors.Wrapf(err, "failed to get hosting serviceaccount %s/%s",
			p.TargetNamespace, p.HostingServiceAccountName)
	}

	existing, err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Get(ctx, p.TargetSecret, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		existing = nil
	case err != nil:
		return Result{}, errors.Wrapf(err, "failed to get managed kubeconfig secret %s/%s", p.TargetNamespace, p.TargetSecret)
	}
	if existing != nil && !metav1.IsControlledBy(existing, hostingServiceAccount) {
		return Result{}, errors.Errorf("managed kubeconfig secret %s/%s is not controlled by serviceaccount %s/%s",
			p.TargetNamespace, p.TargetSecret, hostingServiceAccount.Namespace, hostingServiceAccount.Name)
	}

	managedClient, err := p.ManagedClientFactory(sourceConfig)
	if err != nil {
		return Result{}, err
	}

	managedServiceAccount, err := managedClient.CoreV1().ServiceAccounts(p.ManagedServiceAccountNamespace).Get(
		ctx,
		p.ManagedServiceAccountName,
		metav1.GetOptions{},
	)
	if err != nil {
		return Result{}, errors.Wrapf(err, "failed to get managed serviceaccount %s/%s",
			p.ManagedServiceAccountNamespace, p.ManagedServiceAccountName)
	}
	managedServiceAccountUID := string(managedServiceAccount.UID)
	if fresh, expiration := p.targetSecretFresh(existing, sourceHash, managedServiceAccountUID); fresh {
		return p.resultForExpiration(expiration), nil
	}

	tokenRequest, err := p.requestToken(ctx, managedClient)
	if err != nil {
		return Result{}, err
	}
	expirationTime := tokenRequest.Status.ExpirationTimestamp.UTC()

	kubeconfig, err := buildManagedKubeconfig(cluster, p.ManagedServiceAccountNamespace, p.ManagedServiceAccountName, managedTokenFilePath)
	if err != nil {
		return Result{}, err
	}

	expiration := expirationTime.Format(time.RFC3339)
	desiredAnnotations := map[string]string{
		tokenExpirationAnnotation:                expiration,
		sourceKubeconfigHashAnnotation:           sourceHash,
		managedServiceAccountNamespaceAnnotation: p.ManagedServiceAccountNamespace,
		managedServiceAccountNameAnnotation:      p.ManagedServiceAccountName,
		ManagedServiceAccountUIDAnnotation:       managedServiceAccountUID,
		tokenExpirationSecondsAnnotation:         strconv.FormatInt(p.TokenExpirationSeconds, 10),
	}
	desiredData := map[string][]byte{
		KubeconfigSecretKey:           kubeconfig,
		corev1.ServiceAccountTokenKey: []byte(tokenRequest.Status.Token),
		tokenExpirationKey:            []byte(expiration),
	}

	secret := p.buildTargetSecret(existing, hostingServiceAccount, desiredAnnotations, desiredData)
	if existing != nil {
		if _, err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return Result{}, errors.Wrapf(err, "failed to update managed kubeconfig secret %s/%s", p.TargetNamespace, p.TargetSecret)
		}
	} else {
		if _, err := p.HostingClient.CoreV1().Secrets(p.TargetNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return Result{}, errors.Wrapf(err, "failed to create managed kubeconfig secret %s/%s", p.TargetNamespace, p.TargetSecret)
		}
	}
	return p.resultForExpiration(expirationTime), nil
}

// loadSourceKubeconfig reads the external managed kubeconfig secret, repairs
// file references from bundled credentials, and rejects non-portable file
// references, returning the parsed config, its current cluster, and the
// canonical cluster hash.
func (p *Provisioner) loadSourceKubeconfig(ctx context.Context) (*clientcmdapi.Config, *clientcmdapi.Cluster, string, error) {
	source, err := p.HostingClient.CoreV1().Secrets(p.SourceNamespace).Get(ctx, p.SourceSecret, metav1.GetOptions{})
	if err != nil {
		return nil, nil, "", errors.Wrapf(err, "failed to get external managed kubeconfig secret %s/%s", p.SourceNamespace, p.SourceSecret)
	}

	sourceKubeconfig := source.Data[KubeconfigSecretKey]
	if len(sourceKubeconfig) == 0 {
		return nil, nil, "", errors.Errorf("external managed kubeconfig secret %s/%s missing %q data", p.SourceNamespace, p.SourceSecret, KubeconfigSecretKey)
	}

	sourceConfig, err := clientcmd.Load(sourceKubeconfig)
	if err != nil {
		return nil, nil, "", errors.Wrapf(err, "failed to load external managed kubeconfig from secret %s/%s", p.SourceNamespace, p.SourceSecret)
	}

	cluster, authInfo, err := currentClusterAndAuthInfo(sourceConfig)
	if err != nil {
		return nil, nil, "", err
	}
	repairAuthInfoFromSecretData(authInfo, source.Data)
	if err := validateClusterIsPortable(cluster); err != nil {
		return nil, nil, "", err
	}
	if err := validateAuthInfoIsPortable(authInfo); err != nil {
		return nil, nil, "", err
	}

	sourceHash, err := sourceKubeconfigHashFromCluster(cluster)
	if err != nil {
		return nil, nil, "", err
	}
	return sourceConfig, cluster, sourceHash, nil
}

// requestToken mints a managed serviceaccount token and verifies its actual
// lifetime leaves room for the configured refresh-before window.
func (p *Provisioner) requestToken(ctx context.Context, managedClient kubernetes.Interface) (*authenticationv1.TokenRequest, error) {
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
		return nil, errors.Wrapf(err, "failed to request token for managed serviceaccount %s/%s",
			p.ManagedServiceAccountNamespace, p.ManagedServiceAccountName)
	}
	if len(tokenRequest.Status.Token) == 0 {
		return nil, errors.Errorf("token request for managed serviceaccount %s/%s returned an empty token",
			p.ManagedServiceAccountNamespace, p.ManagedServiceAccountName)
	}
	expirationTime := tokenRequest.Status.ExpirationTimestamp.UTC()
	refreshAt := expirationTime.Add(-p.RefreshBefore)
	if !refreshAt.After(p.Clock.Now().UTC()) {
		return nil, errors.Errorf(
			"token request for managed serviceaccount %s/%s expires at %s, which does not allow the configured refresh-before duration %s",
			p.ManagedServiceAccountNamespace, p.ManagedServiceAccountName, expirationTime.Format(time.RFC3339), p.RefreshBefore)
	}
	return tokenRequest, nil
}

func (p *Provisioner) buildTargetSecret(
	existing *corev1.Secret,
	hostingServiceAccount *corev1.ServiceAccount,
	annotations map[string]string,
	data map[string][]byte,
) *corev1.Secret {
	var secret *corev1.Secret
	if existing != nil {
		secret = existing.DeepCopy()
	} else {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            p.TargetSecret,
				Namespace:       p.TargetNamespace,
				OwnerReferences: []metav1.OwnerReference{serviceAccountOwnerReference(hostingServiceAccount)},
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

// Complete fills in defaults and validates the configuration. It must be called
// before Sync.
func (p *Provisioner) Complete() error {
	if p.HostingClient == nil {
		return errors.New("hosting client is required")
	}
	if len(p.SourceNamespace) == 0 {
		return errors.New("source namespace is required")
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
	if p.SourceNamespace == p.TargetNamespace && p.SourceSecret == p.TargetSecret {
		return errors.Errorf("managed kubeconfig secret %s/%s must differ from the external managed kubeconfig secret %s/%s",
			p.TargetNamespace, p.TargetSecret, p.SourceNamespace, p.SourceSecret)
	}
	if len(p.ManagedServiceAccountNamespace) == 0 {
		p.ManagedServiceAccountNamespace = p.TargetNamespace
	}
	if len(p.ManagedServiceAccountName) == 0 {
		p.ManagedServiceAccountName = DefaultManagedServiceAccountName
	}
	if len(p.HostingServiceAccountName) == 0 {
		p.HostingServiceAccountName = DefaultHostingServiceAccountName
	}
	if p.TokenExpirationSeconds == 0 {
		p.TokenExpirationSeconds = DefaultTokenExpirationSeconds
	}
	if p.RefreshBefore == 0 {
		p.RefreshBefore = DefaultRefreshBefore
	}
	if p.SyncInterval == 0 {
		p.SyncInterval = DefaultSyncInterval
	}
	if err := ValidateRotationSettings(p.TokenExpirationSeconds, p.RefreshBefore, p.SyncInterval); err != nil {
		return err
	}
	if p.ManagedClientFactory == nil {
		p.ManagedClientFactory = newManagedClient
	}
	if p.Clock == nil {
		p.Clock = clock.RealClock{}
	}
	return nil
}

// targetSecretFresh reports whether the target secret can be reused as is, and
// returns the token expiration it was validated against.
func (p *Provisioner) targetSecretFresh(secret *corev1.Secret, sourceHash, managedServiceAccountUID string) (bool, time.Time) {
	if secret == nil || secret.Annotations == nil {
		return false, time.Time{}
	}
	if secret.Annotations[sourceKubeconfigHashAnnotation] != sourceHash ||
		secret.Annotations[managedServiceAccountNamespaceAnnotation] != p.ManagedServiceAccountNamespace ||
		secret.Annotations[managedServiceAccountNameAnnotation] != p.ManagedServiceAccountName ||
		secret.Annotations[ManagedServiceAccountUIDAnnotation] != managedServiceAccountUID ||
		secret.Annotations[tokenExpirationSecondsAnnotation] != strconv.FormatInt(p.TokenExpirationSeconds, 10) {
		return false, time.Time{}
	}
	if len(secret.Data[KubeconfigSecretKey]) == 0 ||
		len(secret.Data[corev1.ServiceAccountTokenKey]) == 0 ||
		len(secret.Data[tokenExpirationKey]) == 0 {
		return false, time.Time{}
	}
	expiration, err := time.Parse(time.RFC3339, secret.Annotations[tokenExpirationAnnotation])
	if err != nil {
		return false, time.Time{}
	}
	if string(secret.Data[tokenExpirationKey]) != expiration.Format(time.RFC3339) {
		return false, time.Time{}
	}
	return expiration.After(p.Clock.Now().UTC().Add(p.RefreshBefore)), expiration
}

func (p *Provisioner) resultForExpiration(expiration time.Time) Result {
	return Result{RequeueAfter: expiration.Add(-p.RefreshBefore).Sub(p.Clock.Now().UTC())}
}

func serviceAccountOwnerReference(serviceAccount *corev1.ServiceAccount) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: corev1.SchemeGroupVersion.String(),
		Kind:       "ServiceAccount",
		Name:       serviceAccount.Name,
		UID:        serviceAccount.UID,
		Controller: ptr.To(true),
	}
}

func buildManagedKubeconfig(cluster *clientcmdapi.Cluster, namespace, serviceAccountName, tokenFile string) ([]byte, error) {
	config := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"managed": cluster.DeepCopy(),
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			serviceAccountName: {
				TokenFile: tokenFile,
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
		config.CurrentContext = contextName
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

// repairAuthInfoFromSecretData embeds client credentials the way the OCM
// operator loads the klusterlet external-managed-kubeconfig secret: when the
// secret bundles tls.crt/tls.key next to the kubeconfig, use them instead of
// the file references so a klusterlet-compatible secret also works here.
func repairAuthInfoFromSecretData(authInfo *clientcmdapi.AuthInfo, data map[string][]byte) {
	if certData, ok := data[corev1.TLSCertKey]; ok && len(authInfo.ClientCertificateData) == 0 {
		authInfo.ClientCertificateData = certData
		authInfo.ClientCertificate = ""
	}
	if keyData, ok := data[corev1.TLSPrivateKeyKey]; ok && len(authInfo.ClientKeyData) == 0 {
		authInfo.ClientKeyData = keyData
		authInfo.ClientKey = ""
	}
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
	if authInfo.AuthProvider != nil {
		return errors.Errorf("external managed kubeconfig user references auth provider %q, which is not portable",
			authInfo.AuthProvider.Name)
	}
	for _, ref := range []struct{ path, description string }{
		{authInfo.ClientCertificate, "client-certificate file"},
		{authInfo.ClientKey, "client-key file"},
		{authInfo.TokenFile, "tokenFile"},
	} {
		if len(ref.path) > 0 {
			return errors.Errorf("external managed kubeconfig user references %s %q, which is not portable",
				ref.description, ref.path)
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
