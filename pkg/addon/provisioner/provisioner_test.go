package provisioner

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	fakekube "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clock "k8s.io/utils/clock/testing"
)

func TestNewManagedClientRequestTimeout(t *testing.T) {
	config := &clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"managed": {Server: "https://managed.example.com"},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"user": {Token: "token"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"managed": {Cluster: "managed", AuthInfo: "user"},
		},
		CurrentContext: "managed",
	}

	client, err := newManagedClient(config)
	assert.NoError(t, err)
	restClient, ok := client.Discovery().RESTClient().(*rest.RESTClient)
	if assert.True(t, ok, "expected concrete REST client") {
		assert.Equal(t, DefaultRequestTimeout, restClient.Client.Timeout)
	}
}

func TestCurrentClusterAndAuthInfoDefaultsSingleContext(t *testing.T) {
	config := &clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"managed": {Server: "https://managed.example.com"},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"user": {Token: "token"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"only-context": {Cluster: "managed", AuthInfo: "user"},
		},
	}

	cluster, authInfo, err := currentClusterAndAuthInfo(config)
	assert.NoError(t, err)
	assert.Same(t, config.Clusters["managed"], cluster)
	assert.Same(t, config.AuthInfos["user"], authInfo)
	assert.Equal(t, "only-context", config.CurrentContext)
	_, err = newManagedClient(config)
	assert.NoError(t, err)
}

func TestRepairAuthInfoFromSecretData(t *testing.T) {
	data := map[string][]byte{
		corev1.TLSCertKey:       []byte("bundled-cert"),
		corev1.TLSPrivateKeyKey: []byte("bundled-key"),
	}
	cases := []struct {
		name     string
		authInfo clientcmdapi.AuthInfo
		expected clientcmdapi.AuthInfo
	}{
		{
			name:     "embeds bundled credentials over file references",
			authInfo: clientcmdapi.AuthInfo{ClientCertificate: "tls.crt", ClientKey: "tls.key"},
			expected: clientcmdapi.AuthInfo{ClientCertificateData: []byte("bundled-cert"), ClientKeyData: []byte("bundled-key")},
		},
		{
			name:     "preserves inline credentials",
			authInfo: clientcmdapi.AuthInfo{ClientCertificateData: []byte("inline-cert"), ClientKeyData: []byte("inline-key")},
			expected: clientcmdapi.AuthInfo{ClientCertificateData: []byte("inline-cert"), ClientKeyData: []byte("inline-key")},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repairAuthInfoFromSecretData(&c.authInfo, data)
			assert.Equal(t, c.expected, c.authInfo)
		})
	}
}

func TestSync(t *testing.T) {
	now := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	portable := testKubeconfig(t, "https://managed.example.com", []byte("ca-1"))
	updatedSource := testKubeconfig(t, "https://managed-new.example.com", []byte("ca-2"))
	caFileSource := testKubeconfigWithCAFile(t, "https://managed.example.com", "/etc/ssl/source-ca.crt")

	cases := []struct {
		name             string
		sourceKubeconfig []byte
		sourceExtraData  map[string][]byte
		existing         *corev1.Secret
		mutate           func(*Provisioner)
		stubToken        func(t *testing.T, client *fakekube.Clientset)
		expectedError    string
		validate         func(t *testing.T, hostingClient, managedClient *fakekube.Clientset)
	}{
		{
			name:          "external managed kubeconfig secret missing",
			expectedError: "failed to get external managed kubeconfig secret source-ns/external-managed-kubeconfig: secrets \"external-managed-kubeconfig\" not found",
		},
		{
			name:             "creates managed kubeconfig secret from service account token",
			sourceKubeconfig: portable,
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequest(t, client, "token-1", now.Add(time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				assert.Equal(t, corev1.SecretTypeOpaque, secret.Type)
				assert.Equal(t, now.Add(time.Hour).Format(time.RFC3339), secret.Annotations[tokenExpirationAnnotation])
				assert.Equal(t, sourceKubeconfigHash(portable), secret.Annotations[sourceKubeconfigHashAnnotation])
				assert.Equal(t, "addon-ns", secret.Annotations[managedServiceAccountNamespaceAnnotation])
				assert.Equal(t, "managed-serviceaccount", secret.Annotations[managedServiceAccountNameAnnotation])
				assert.Equal(t, "managed-serviceaccount-uid", secret.Annotations[ManagedServiceAccountUIDAnnotation])
				assert.Equal(t, "3600", secret.Annotations[tokenExpirationSecondsAnnotation])

				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, "https://managed.example.com", kubeconfig.Clusters["managed"].Server)
				assert.Equal(t, []byte("ca-1"), kubeconfig.Clusters["managed"].CertificateAuthorityData)
				assert.Empty(t, kubeconfig.AuthInfos["managed-serviceaccount"].Token)
				assert.Equal(t, managedTokenFilePath, kubeconfig.AuthInfos["managed-serviceaccount"].TokenFile)
				assert.Equal(t, []byte("token-1"), secret.Data[corev1.ServiceAccountTokenKey])
				assert.Equal(t, "managed", kubeconfig.CurrentContext)
				assertControlledByHostingServiceAccount(t, secret)
			},
		},
		{
			name:             "skips refresh when token is still valid and source cluster info unchanged",
			sourceKubeconfig: portable,
			existing:         freshTargetSecret(now.Add(2*time.Hour), portable),
			validate: func(t *testing.T, hostingClient, managedClient *fakekube.Clientset) {
				assertNoAction(t, hostingClient.Actions(), "update", "secrets")
				assertNoAction(t, managedClient.Actions(), "create", "serviceaccounts/token")
			},
		},
		{
			name:             "refreshes when managed serviceaccount identity changes",
			sourceKubeconfig: portable,
			existing:         freshTargetSecret(now.Add(2*time.Hour), portable),
			mutate: func(o *Provisioner) {
				o.ManagedServiceAccountNamespace = "other-ns"
				o.ManagedServiceAccountName = "renamed-sa"
			},
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequestFor(t, client, "other-ns", "renamed-sa", 3600, "token-rename", now.Add(time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				assert.Equal(t, "other-ns", secret.Annotations[managedServiceAccountNamespaceAnnotation])
				assert.Equal(t, "renamed-sa", secret.Annotations[managedServiceAccountNameAnnotation])

				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, managedTokenFilePath, kubeconfig.AuthInfos["renamed-sa"].TokenFile)
				assert.Equal(t, []byte("token-rename"), secret.Data[corev1.ServiceAccountTokenKey])
				assert.Equal(t, "other-ns", kubeconfig.Contexts["managed"].Namespace)
			},
		},
		{
			name:             "refreshes when managed serviceaccount is recreated",
			sourceKubeconfig: portable,
			existing: freshTargetSecret(now.Add(2*time.Hour), portable, func(secret *corev1.Secret) {
				secret.Annotations[ManagedServiceAccountUIDAnnotation] = "old-managed-serviceaccount-uid"
			}),
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequest(t, client, "token-recreated", now.Add(time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				assert.Equal(t, "managed-serviceaccount-uid", secret.Annotations[ManagedServiceAccountUIDAnnotation])

				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, managedTokenFilePath, kubeconfig.AuthInfos["managed-serviceaccount"].TokenFile)
				assert.Equal(t, []byte("token-recreated"), secret.Data[corev1.ServiceAccountTokenKey])
			},
		},
		{
			name:             "refreshes when token expiration seconds changes",
			sourceKubeconfig: portable,
			existing:         freshTargetSecret(now.Add(2*time.Hour), portable),
			mutate: func(o *Provisioner) {
				o.TokenExpirationSeconds = 14400
			},
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequestFor(t, client, "addon-ns", "managed-serviceaccount", 14400, "token-longer", now.Add(4*time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				assert.Equal(t, "14400", secret.Annotations[tokenExpirationSecondsAnnotation])
				assert.Equal(t, now.Add(4*time.Hour).Format(time.RFC3339), secret.Annotations[tokenExpirationAnnotation])
			},
		},
		{
			name:             "refreshes when token is expiring",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				map[string]string{
					tokenExpirationAnnotation:      now.Add(5 * time.Minute).Format(time.RFC3339),
					sourceKubeconfigHashAnnotation: sourceKubeconfigHash(portable),
				},
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
				},
			),
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequest(t, client, "token-2", now.Add(time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, managedTokenFilePath, kubeconfig.AuthInfos["managed-serviceaccount"].TokenFile)
				assert.Equal(t, []byte("token-2"), secret.Data[corev1.ServiceAccountTokenKey])
				assert.Equal(t, now.Add(time.Hour).Format(time.RFC3339), secret.Annotations[tokenExpirationAnnotation])
			},
		},
		{
			name:             "refreshes when source cluster info changes",
			sourceKubeconfig: updatedSource,
			existing: existingTargetSecret(
				map[string]string{
					tokenExpirationAnnotation:      now.Add(2 * time.Hour).Format(time.RFC3339),
					sourceKubeconfigHashAnnotation: "old-hash",
				},
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
				},
			),
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequest(t, client, "token-3", now.Add(time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, "https://managed-new.example.com", kubeconfig.Clusters["managed"].Server)
				assert.Equal(t, []byte("ca-2"), kubeconfig.Clusters["managed"].CertificateAuthorityData)
				assert.Equal(t, managedTokenFilePath, kubeconfig.AuthInfos["managed-serviceaccount"].TokenFile)
				assert.Equal(t, []byte("token-3"), secret.Data[corev1.ServiceAccountTokenKey])
				assert.Equal(t, sourceKubeconfigHash(updatedSource), secret.Annotations[sourceKubeconfigHashAnnotation])
			},
		},
		{
			name:             "refreshes when target secret data is missing",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				map[string]string{
					tokenExpirationAnnotation:      now.Add(2 * time.Hour).Format(time.RFC3339),
					sourceKubeconfigHashAnnotation: sourceKubeconfigHash(portable),
				},
				map[string][]byte{},
			),
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequest(t, client, "token-repair", now.Add(time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				assert.NotEmpty(t, secret.Data[KubeconfigSecretKey])
				assert.Equal(t, now.Add(time.Hour).Format(time.RFC3339), string(secret.Data[tokenExpirationKey]))

				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, managedTokenFilePath, kubeconfig.AuthInfos["managed-serviceaccount"].TokenFile)
				assert.Equal(t, []byte("token-repair"), secret.Data[corev1.ServiceAccountTokenKey])
			},
		},
		{
			name:             "rejects source kubeconfig with file-based certificate authority",
			sourceKubeconfig: caFileSource,
			expectedError:    "external managed kubeconfig cluster references certificate-authority file \"/etc/ssl/source-ca.crt\", which is not portable",
		},
		{
			name:             "rejects source kubeconfig with file-based certificate authority even when target secret is fresh",
			sourceKubeconfig: caFileSource,
			existing:         freshTargetSecret(now.Add(2*time.Hour), caFileSource),
			expectedError:    "external managed kubeconfig cluster references certificate-authority file \"/etc/ssl/source-ca.crt\", which is not portable",
		},
		{
			name: "repairs file-based client credentials from bundled tls.crt and tls.key",
			sourceKubeconfig: testKubeconfigWithAuthInfo(t, "https://managed.example.com", []byte("ca-1"),
				clientcmdapi.AuthInfo{ClientCertificate: "tls.crt", ClientKey: "tls.key"}),
			sourceExtraData: map[string][]byte{
				corev1.TLSCertKey:       []byte("bundled-cert"),
				corev1.TLSPrivateKeyKey: []byte("bundled-key"),
			},
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequest(t, client, "token-repaired", now.Add(time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, managedTokenFilePath, kubeconfig.AuthInfos["managed-serviceaccount"].TokenFile)
				assert.Empty(t, kubeconfig.AuthInfos["managed-serviceaccount"].ClientCertificateData)
				assert.Equal(t, []byte("token-repaired"), secret.Data[corev1.ServiceAccountTokenKey])
			},
		},
		{
			name:             "rejects source kubeconfig with client-certificate file",
			sourceKubeconfig: testKubeconfigWithAuthInfo(t, "https://managed.example.com", []byte("ca-1"), clientcmdapi.AuthInfo{ClientCertificate: "/etc/creds/client.crt", ClientKeyData: []byte("key")}),
			expectedError:    "external managed kubeconfig user references client-certificate file \"/etc/creds/client.crt\", which is not portable",
		},
		{
			name:             "rejects source kubeconfig with client-key file",
			sourceKubeconfig: testKubeconfigWithAuthInfo(t, "https://managed.example.com", []byte("ca-1"), clientcmdapi.AuthInfo{ClientCertificateData: []byte("crt"), ClientKey: "/etc/creds/client.key"}),
			expectedError:    "external managed kubeconfig user references client-key file \"/etc/creds/client.key\", which is not portable",
		},
		{
			name:             "rejects source kubeconfig with token file",
			sourceKubeconfig: testKubeconfigWithAuthInfo(t, "https://managed.example.com", []byte("ca-1"), clientcmdapi.AuthInfo{TokenFile: "/var/run/secrets/token"}),
			expectedError:    "external managed kubeconfig user references tokenFile \"/var/run/secrets/token\", which is not portable",
		},
		{
			name: "rejects source kubeconfig with exec credential plugin",
			sourceKubeconfig: testKubeconfigWithAuthInfo(t, "https://managed.example.com", []byte("ca-1"), clientcmdapi.AuthInfo{
				Exec: &clientcmdapi.ExecConfig{
					Command:    "gke-gcloud-auth-plugin",
					APIVersion: "client.authentication.k8s.io/v1beta1",
				},
			}),
			expectedError: "external managed kubeconfig user references exec credential plugin \"gke-gcloud-auth-plugin\", which is not portable",
		},
		{
			name: "rejects source kubeconfig with auth provider",
			sourceKubeconfig: testKubeconfigWithAuthInfo(t, "https://managed.example.com", []byte("ca-1"), clientcmdapi.AuthInfo{
				AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "gcp"},
			}),
			expectedError: "external managed kubeconfig user references auth provider \"gcp\", which is not portable",
		},
		{
			name:             "rejects unowned managed kubeconfig secret",
			sourceKubeconfig: portable,
			existing: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name: "target-kubeconfig", Namespace: "addon-ns",
			}},
			expectedError: "managed kubeconfig secret addon-ns/target-kubeconfig is not controlled by serviceaccount addon-ns/managed-serviceaccount-kubeconfig-provisioner",
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				assertNoAction(t, hostingClient.Actions(), "create", "secrets")
				assertNoAction(t, hostingClient.Actions(), "update", "secrets")
			},
		},
		{
			name:             "rejects token lifetime shorter than refresh window",
			sourceKubeconfig: portable,
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequest(t, client, "too-short", now.Add(20*time.Minute))
			},
			expectedError: "token request for managed serviceaccount addon-ns/managed-serviceaccount expires at 2026-05-13T00:20:00Z, which does not allow the configured refresh-before duration 30m0s",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			objs := []runtime.Object{hostingServiceAccount()}
			if c.sourceKubeconfig != nil {
				source := newSourceSecret(c.sourceKubeconfig)
				for key, value := range c.sourceExtraData {
					source.Data[key] = value
				}
				objs = append(objs, source)
			}
			if c.existing != nil {
				objs = append(objs, c.existing)
			}
			hostingClient := fakekube.NewSimpleClientset(objs...)
			managedClient := fakekube.NewSimpleClientset(
				managedServiceAccount("addon-ns", "managed-serviceaccount", "managed-serviceaccount-uid"),
				managedServiceAccount("other-ns", "renamed-sa", "renamed-sa-uid"),
			)
			if c.stubToken != nil {
				c.stubToken(t, managedClient)
			}
			p := newTestProvisioner(hostingClient, managedClient, func(o *Provisioner) {
				o.Clock = clock.NewFakeClock(now)
				if c.mutate != nil {
					c.mutate(o)
				}
			})

			_, err := p.Sync(context.Background())

			if len(c.expectedError) > 0 {
				assert.EqualError(t, err, c.expectedError)
			} else {
				assert.NoError(t, err)
			}
			if c.validate != nil {
				c.validate(t, hostingClient, managedClient)
			}
		})
	}
}

func TestSyncSchedulesRefreshFromActualExpiration(t *testing.T) {
	now := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	portable := testKubeconfig(t, "https://managed.example.com", []byte("ca-1"))
	hostingClient := fakekube.NewSimpleClientset(
		hostingServiceAccount(),
		newSourceSecret(portable),
	)
	managedClient := fakekube.NewSimpleClientset(
		managedServiceAccount("addon-ns", "managed-serviceaccount", "managed-serviceaccount-uid"),
	)
	stubTokenRequest(t, managedClient, "token", now.Add(45*time.Minute))
	p := newTestProvisioner(hostingClient, managedClient, func(p *Provisioner) {
		p.Clock = clock.NewFakeClock(now)
		p.RefreshBefore = 10 * time.Minute
	})

	result, err := p.Sync(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, 35*time.Minute, result.RequeueAfter)
}

func TestRunReconcile(t *testing.T) {
	cases := []struct {
		name              string
		cancelBeforeStart bool
		onSync            func(count int64, cancel context.CancelFunc) (Result, error)
		syncInterval      time.Duration
		minSyncCalls      int64
		maxElapsed        time.Duration
	}{
		{
			name: "syncs until the context is cancelled",
			onSync: func(count int64, cancel context.CancelFunc) (Result, error) {
				if count >= 3 {
					cancel()
				}
				return Result{}, nil
			},
			syncInterval: time.Millisecond,
			minSyncCalls: 3,
		},
		{
			name: "continues after a sync error",
			onSync: func(count int64, cancel context.CancelFunc) (Result, error) {
				if count >= 2 {
					cancel()
					return Result{}, nil
				}
				return Result{}, fmt.Errorf("transient sync error %d", count)
			},
			syncInterval: time.Millisecond,
			minSyncCalls: 2,
		},
		{
			// a transient failure must retry on the error backoff, not the full sync interval
			name: "retries quickly after a transient error",
			onSync: func(count int64, cancel context.CancelFunc) (Result, error) {
				if count == 1 {
					return Result{}, errors.New("transient")
				}
				cancel()
				return Result{}, nil
			},
			syncInterval: time.Hour,
			minSyncCalls: 2,
		},
		{
			name: "uses the token refresh deadline before the sync interval",
			onSync: func(count int64, cancel context.CancelFunc) (Result, error) {
				if count >= 2 {
					cancel()
				}
				return Result{RequeueAfter: time.Millisecond}, nil
			},
			syncInterval: time.Hour,
			minSyncCalls: 2,
			maxElapsed:   5 * time.Second,
		},
		{
			name:              "returns promptly when the context is already cancelled",
			cancelBeforeStart: true,
			syncInterval:      time.Hour,
			maxElapsed:        5 * time.Second,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.cancelBeforeStart {
				cancel()
			}
			stub := &stubSyncer{}
			if c.onSync != nil {
				stub.onSync = func(count int64) (Result, error) {
					return c.onSync(count, cancel)
				}
			}

			start := time.Now()
			done := make(chan error, 1)
			go func() {
				done <- runReconcile(ctx, stub.Sync, c.syncInterval)
			}()

			select {
			case err := <-done:
				assert.NoError(t, err)
			case <-time.After(30 * time.Second):
				t.Fatal("runReconcile did not return after context cancellation")
			}

			syncCalls := atomic.LoadInt64(&stub.syncCalls)
			assert.GreaterOrEqual(t, syncCalls, c.minSyncCalls)
			if c.maxElapsed > 0 {
				assert.Less(t, time.Since(start), c.maxElapsed)
			}
		})
	}
}

type stubSyncer struct {
	syncCalls int64
	onSync    func(count int64) (Result, error)
}

func (s *stubSyncer) Sync(ctx context.Context) (Result, error) {
	count := atomic.AddInt64(&s.syncCalls, 1)
	if s.onSync == nil {
		return Result{}, nil
	}
	return s.onSync(count)
}

func TestCompleteRejectsInvalidConfig(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Provisioner)
	}{
		{
			name:   "missing hosting client",
			mutate: func(o *Provisioner) { o.HostingClient = nil },
		},
		{
			name:   "empty source namespace",
			mutate: func(o *Provisioner) { o.SourceNamespace = "" },
		},
		{
			name:   "empty target namespace",
			mutate: func(o *Provisioner) { o.TargetNamespace = "" },
		},
		{
			name:   "empty target secret",
			mutate: func(o *Provisioner) { o.TargetSecret = "" },
		},
		{
			name:   "invalid rotation settings",
			mutate: func(o *Provisioner) { o.TokenExpirationSeconds = minTokenExpirationSeconds - 1 },
		},
		{
			name:   "target secret colliding with source",
			mutate: func(o *Provisioner) { o.TargetNamespace = "source-ns"; o.TargetSecret = "external-managed-kubeconfig" },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestProvisioner(fakekube.NewSimpleClientset(), fakekube.NewSimpleClientset(), c.mutate)
			assert.Error(t, p.Complete())
		})
	}
}

func TestValidateRotationSettings(t *testing.T) {
	cases := []struct {
		name                   string
		tokenExpirationSeconds int64
		refreshBefore          time.Duration
		syncInterval           time.Duration
		expectedError          bool
	}{
		{
			name:                   "valid settings",
			tokenExpirationSeconds: 3600,
			refreshBefore:          10 * time.Minute,
			syncInterval:           5 * time.Minute,
		},
		{
			name:                   "minimum token expiration",
			tokenExpirationSeconds: minTokenExpirationSeconds,
			refreshBefore:          time.Second,
			syncInterval:           time.Minute,
		},
		{
			name:                   "maximum token expiration",
			tokenExpirationSeconds: maxTokenExpirationSeconds,
			refreshBefore:          time.Minute,
			syncInterval:           time.Minute,
		},
		{
			name:                   "token expiration below Kubernetes minimum",
			tokenExpirationSeconds: minTokenExpirationSeconds - 1,
			refreshBefore:          time.Minute,
			syncInterval:           time.Minute,
			expectedError:          true,
		},
		{
			name:                   "token expiration above Kubernetes maximum",
			tokenExpirationSeconds: maxTokenExpirationSeconds + 1,
			refreshBefore:          time.Minute,
			syncInterval:           time.Minute,
			expectedError:          true,
		},
		{
			name:                   "zero refresh before",
			tokenExpirationSeconds: 3600,
			refreshBefore:          0,
			syncInterval:           time.Minute,
			expectedError:          true,
		},
		{
			name:                   "negative refresh before",
			tokenExpirationSeconds: 3600,
			refreshBefore:          -time.Second,
			syncInterval:           time.Minute,
			expectedError:          true,
		},
		{
			name:                   "zero sync interval",
			tokenExpirationSeconds: 3600,
			refreshBefore:          time.Minute,
			syncInterval:           0,
			expectedError:          true,
		},
		{
			name:                   "negative sync interval",
			tokenExpirationSeconds: 3600,
			refreshBefore:          time.Minute,
			syncInterval:           -time.Second,
			expectedError:          true,
		},
		{
			name:                   "refresh equals token lifetime",
			tokenExpirationSeconds: 600,
			refreshBefore:          10 * time.Minute,
			syncInterval:           time.Minute,
			expectedError:          true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateRotationSettings(c.tokenExpirationSeconds, c.refreshBefore, c.syncInterval)
			if c.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCompleteAppliesDefaults(t *testing.T) {
	p := &Provisioner{
		HostingClient:   fakekube.NewSimpleClientset(),
		SourceNamespace: "source-ns",
		TargetNamespace: "addon-ns",
		TargetSecret:    "target-kubeconfig",
	}

	assert.NoError(t, p.Complete())

	assert.Equal(t, DefaultExternalManagedKubeConfigSecret, p.SourceSecret)
	assert.Equal(t, "addon-ns", p.ManagedServiceAccountNamespace)
	assert.Equal(t, DefaultManagedServiceAccountName, p.ManagedServiceAccountName)
	assert.Equal(t, DefaultHostingServiceAccountName, p.HostingServiceAccountName)
	assert.Equal(t, DefaultTokenExpirationSeconds, p.TokenExpirationSeconds)
	assert.Equal(t, DefaultRefreshBefore, p.RefreshBefore)
	assert.Equal(t, DefaultSyncInterval, p.SyncInterval)
}

func newTestProvisioner(hostingClient *fakekube.Clientset, managedClient *fakekube.Clientset, mutate func(*Provisioner)) *Provisioner {
	p := &Provisioner{
		HostingClient:                  hostingClient,
		SourceNamespace:                "source-ns",
		SourceSecret:                   "external-managed-kubeconfig",
		TargetNamespace:                "addon-ns",
		TargetSecret:                   "target-kubeconfig",
		ManagedServiceAccountNamespace: "addon-ns",
		ManagedServiceAccountName:      "managed-serviceaccount",
		HostingServiceAccountName:      DefaultHostingServiceAccountName,
		TokenExpirationSeconds:         3600,
		RefreshBefore:                  30 * time.Minute,
		ManagedClientFactory: func(*clientcmdapi.Config) (kubernetes.Interface, error) {
			return managedClient, nil
		},
	}
	if mutate != nil {
		mutate(p)
	}
	return p
}

func newSourceSecret(kubeconfig []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-managed-kubeconfig",
			Namespace: "source-ns",
		},
		Data: map[string][]byte{
			KubeconfigSecretKey: kubeconfig,
		},
	}
}

func managedServiceAccount(namespace, name, uid string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			UID:       types.UID(uid),
		},
	}
}

func hostingServiceAccount() *corev1.ServiceAccount {
	return managedServiceAccount("addon-ns", DefaultHostingServiceAccountName, "hosting-serviceaccount-uid")
}

// existingTargetSecret builds a pre-existing target secret at addon-ns/target-kubeconfig.
func existingTargetSecret(annotations map[string]string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "target-kubeconfig",
			Namespace:       "addon-ns",
			Annotations:     annotations,
			OwnerReferences: []metav1.OwnerReference{serviceAccountOwnerReference(hostingServiceAccount())},
		},
		Data: data,
	}
}

func assertControlledByHostingServiceAccount(t *testing.T, object metav1.Object) {
	t.Helper()
	owner := metav1.GetControllerOf(object)
	if assert.NotNil(t, owner) {
		assert.Equal(t, "v1", owner.APIVersion)
		assert.Equal(t, "ServiceAccount", owner.Kind)
		assert.Equal(t, DefaultHostingServiceAccountName, owner.Name)
		assert.Equal(t, types.UID("hosting-serviceaccount-uid"), owner.UID)
	}
}

// freshTargetSecret builds a target secret that Sync considers up to date.
func freshTargetSecret(expires time.Time, sourceKubeconfig []byte, modifiers ...func(*corev1.Secret)) *corev1.Secret {
	secret := existingTargetSecret(
		freshTargetAnnotations(expires, sourceKubeconfig),
		map[string][]byte{
			KubeconfigSecretKey:           []byte("existing"),
			corev1.ServiceAccountTokenKey: []byte("existing-token"),
			tokenExpirationKey:            []byte(expires.Format(time.RFC3339)),
		},
	)
	for _, modify := range modifiers {
		modify(secret)
	}
	return secret
}

// freshTargetAnnotations returns the annotations the provisioner stamps for the default
// managed serviceaccount identity, which mark the target secret as up to date.
func freshTargetAnnotations(expires time.Time, sourceKubeconfig []byte) map[string]string {
	return map[string]string{
		tokenExpirationAnnotation:                expires.Format(time.RFC3339),
		sourceKubeconfigHashAnnotation:           sourceKubeconfigHash(sourceKubeconfig),
		managedServiceAccountNamespaceAnnotation: "addon-ns",
		managedServiceAccountNameAnnotation:      "managed-serviceaccount",
		ManagedServiceAccountUIDAnnotation:       "managed-serviceaccount-uid",
		tokenExpirationSecondsAnnotation:         "3600",
	}
}

func getTargetSecret(t *testing.T, hostingClient *fakekube.Clientset) *corev1.Secret {
	t.Helper()
	secret, err := hostingClient.CoreV1().Secrets("addon-ns").Get(context.Background(), "target-kubeconfig", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get target secret: %v", err)
	}
	return secret
}

func loadTargetKubeconfig(t *testing.T, secret *corev1.Secret) *clientcmdapi.Config {
	t.Helper()
	kubeconfig, err := clientcmd.Load(secret.Data[KubeconfigSecretKey])
	if err != nil {
		t.Fatalf("failed to load target kubeconfig: %v", err)
	}
	return kubeconfig
}

func testKubeconfig(t *testing.T, server string, ca []byte) []byte {
	t.Helper()
	return testKubeconfigWithAuthInfo(t, server, ca, clientcmdapi.AuthInfo{Token: "source-token"})
}

func testKubeconfigWithCAFile(t *testing.T, server, caFile string) []byte {
	t.Helper()
	return writeTestKubeconfig(t, clientcmdapi.Cluster{
		Server:               server,
		CertificateAuthority: caFile,
	}, clientcmdapi.AuthInfo{Token: "source-token"})
}

func testKubeconfigWithAuthInfo(t *testing.T, server string, ca []byte, authInfo clientcmdapi.AuthInfo) []byte {
	t.Helper()
	return writeTestKubeconfig(t, clientcmdapi.Cluster{
		Server:                   server,
		CertificateAuthorityData: ca,
	}, authInfo)
}

func writeTestKubeconfig(t *testing.T, cluster clientcmdapi.Cluster, authInfo clientcmdapi.AuthInfo) []byte {
	t.Helper()

	data, err := clientcmd.Write(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"managed": &cluster},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"source": &authInfo},
		Contexts:       map[string]*clientcmdapi.Context{"managed": {Cluster: "managed", AuthInfo: "source"}},
		CurrentContext: "managed",
	})
	assert.NoError(t, err)
	return data
}

func stubTokenRequest(t *testing.T, client *fakekube.Clientset, token string, expires time.Time) {
	t.Helper()
	stubTokenRequestFor(t, client, "addon-ns", "managed-serviceaccount", 3600, token, expires)
}

func stubTokenRequestFor(t *testing.T, client *fakekube.Clientset, namespace, name string, expirationSeconds int64, token string, expires time.Time) {
	t.Helper()

	client.PrependReactor("create", "serviceaccounts/token", func(action clienttesting.Action) (bool, runtime.Object, error) {
		createAction := action.(clienttesting.CreateAction)
		assert.Equal(t, namespace, action.GetNamespace())
		assert.Equal(t, name, action.(clienttesting.CreateActionImpl).Name)
		request := createAction.GetObject().(*authenticationv1.TokenRequest)
		assert.Equal(t, expirationSeconds, *request.Spec.ExpirationSeconds)
		return true, &authenticationv1.TokenRequest{
			Status: authenticationv1.TokenRequestStatus{
				Token:               token,
				ExpirationTimestamp: metav1.NewTime(expires),
			},
		}, nil
	})
}

func assertNoAction(t *testing.T, actions []clienttesting.Action, verb, resource string) {
	t.Helper()

	for _, action := range actions {
		if action.Matches(verb, resource) {
			t.Fatalf("unexpected action %s %s: %#v", verb, resource, action)
		}
	}
}

func sourceKubeconfigHash(kubeconfig []byte) string {
	config, err := clientcmd.Load(kubeconfig)
	if err != nil {
		panic(err)
	}
	cluster, _, err := currentClusterAndAuthInfo(config)
	if err != nil {
		panic(err)
	}
	hash, err := sourceKubeconfigHashFromCluster(cluster)
	if err != nil {
		panic(err)
	}
	return hash
}
