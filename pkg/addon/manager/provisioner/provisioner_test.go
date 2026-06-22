package provisioner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	fakekube "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clock "k8s.io/utils/clock/testing"
)

func TestSync(t *testing.T) {
	now := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	portable := testKubeconfig(t, "https://managed.example.com", []byte("ca-1"))
	updatedSource := testKubeconfig(t, "https://managed-new.example.com", []byte("ca-2"))
	caFileSource := testKubeconfigWithCAFile(t, "https://managed.example.com", "/etc/ssl/source-ca.crt")

	assertNoSecretWrite := func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
		assertNoAction(t, hostingClient.Actions(), "create", "secrets")
		assertNoAction(t, hostingClient.Actions(), "update", "secrets")
	}

	cases := []struct {
		name             string
		sourceKubeconfig []byte
		existing         *corev1.Secret
		existingHub      *corev1.Secret
		managedSecret    *corev1.Secret
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
				assert.Equal(t, now.Add(time.Hour).Format(time.RFC3339), secret.Annotations[TokenExpirationAnnotation])
				assert.Equal(t, sourceKubeconfigHash(portable), secret.Annotations[SourceKubeconfigHashAnnotation])
				assert.Equal(t, "addon-ns", secret.Annotations[ManagedServiceAccountNamespaceAnnotation])
				assert.Equal(t, "managed-serviceaccount", secret.Annotations[ManagedServiceAccountNameAnnotation])
				assert.Equal(t, "3600", secret.Annotations[TokenExpirationSecondsAnnotation])

				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, "https://managed.example.com", kubeconfig.Clusters["managed"].Server)
				assert.Equal(t, []byte("ca-1"), kubeconfig.Clusters["managed"].CertificateAuthorityData)
				assert.Equal(t, "token-1", kubeconfig.AuthInfos["managed-serviceaccount"].Token)
				assert.Equal(t, "managed", kubeconfig.CurrentContext)
			},
		},
		{
			name:             "skips refresh when token is still valid and source cluster info unchanged",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				freshTargetAnnotations(now.Add(2*time.Hour), portable),
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
					TokenExpirationKey:  []byte(now.Add(2 * time.Hour).Format(time.RFC3339)),
				},
			),
			validate: func(t *testing.T, hostingClient, managedClient *fakekube.Clientset) {
				assertNoAction(t, hostingClient.Actions(), "update", "secrets")
				assertNoAction(t, managedClient.Actions(), "create", "serviceaccounts/token")
			},
		},
		{
			name:             "refreshes when managed serviceaccount identity changes",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				freshTargetAnnotations(now.Add(2*time.Hour), portable),
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
					TokenExpirationKey:  []byte(now.Add(2 * time.Hour).Format(time.RFC3339)),
				},
			),
			mutate: func(o *Provisioner) {
				o.ManagedServiceAccountNamespace = "other-ns"
				o.ManagedServiceAccountName = "renamed-sa"
			},
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequestFor(t, client, "other-ns", "renamed-sa", 3600, "token-rename", now.Add(time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				assert.Equal(t, "other-ns", secret.Annotations[ManagedServiceAccountNamespaceAnnotation])
				assert.Equal(t, "renamed-sa", secret.Annotations[ManagedServiceAccountNameAnnotation])

				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, "token-rename", kubeconfig.AuthInfos["renamed-sa"].Token)
				assert.Equal(t, "other-ns", kubeconfig.Contexts["managed"].Namespace)
				assertAction(t, hostingClient.Actions(), "update", "secrets")
			},
		},
		{
			name:             "refreshes when token expiration seconds changes",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				freshTargetAnnotations(now.Add(2*time.Hour), portable),
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
					TokenExpirationKey:  []byte(now.Add(2 * time.Hour).Format(time.RFC3339)),
				},
			),
			mutate: func(o *Provisioner) {
				o.TokenExpirationSeconds = 14400
			},
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequestFor(t, client, "addon-ns", "managed-serviceaccount", 14400, "token-longer", now.Add(4*time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				assert.Equal(t, "14400", secret.Annotations[TokenExpirationSecondsAnnotation])
				assert.Equal(t, now.Add(4*time.Hour).Format(time.RFC3339), secret.Annotations[TokenExpirationAnnotation])
				assertAction(t, hostingClient.Actions(), "update", "secrets")
			},
		},
		{
			name:             "refreshes when token is expiring",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				map[string]string{
					TokenExpirationAnnotation:      now.Add(5 * time.Minute).Format(time.RFC3339),
					SourceKubeconfigHashAnnotation: sourceKubeconfigHash(portable),
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
				assert.Equal(t, "token-2", kubeconfig.AuthInfos["managed-serviceaccount"].Token)
				assert.Equal(t, now.Add(time.Hour).Format(time.RFC3339), secret.Annotations[TokenExpirationAnnotation])
				assertAction(t, hostingClient.Actions(), "update", "secrets")
			},
		},
		{
			name:             "refreshes when source cluster info changes",
			sourceKubeconfig: updatedSource,
			existing: existingTargetSecret(
				map[string]string{
					TokenExpirationAnnotation:      now.Add(2 * time.Hour).Format(time.RFC3339),
					SourceKubeconfigHashAnnotation: "old-hash",
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
				assert.Equal(t, "token-3", kubeconfig.AuthInfos["managed-serviceaccount"].Token)
				assert.Equal(t, sourceKubeconfigHash(updatedSource), secret.Annotations[SourceKubeconfigHashAnnotation])
			},
		},
		{
			name:             "refreshes when target secret data is missing",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				map[string]string{
					TokenExpirationAnnotation:      now.Add(2 * time.Hour).Format(time.RFC3339),
					SourceKubeconfigHashAnnotation: sourceKubeconfigHash(portable),
				},
				map[string][]byte{},
			),
			stubToken: func(t *testing.T, client *fakekube.Clientset) {
				stubTokenRequest(t, client, "token-repair", now.Add(time.Hour))
			},
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret := getTargetSecret(t, hostingClient)
				assert.NotEmpty(t, secret.Data[KubeconfigSecretKey])
				assert.Equal(t, now.Add(time.Hour).Format(time.RFC3339), string(secret.Data[TokenExpirationKey]))

				kubeconfig := loadTargetKubeconfig(t, secret)
				assert.Equal(t, "token-repair", kubeconfig.AuthInfos["managed-serviceaccount"].Token)
				assertAction(t, hostingClient.Actions(), "update", "secrets")
			},
		},
		{
			name:             "rejects source kubeconfig with file-based certificate authority",
			sourceKubeconfig: caFileSource,
			expectedError:    "external managed kubeconfig cluster references certificate-authority file \"/etc/ssl/source-ca.crt\", which is not portable",
			validate:         assertNoSecretWrite,
		},
		{
			name:             "rejects source kubeconfig with file-based certificate authority even when target secret is fresh",
			sourceKubeconfig: caFileSource,
			existing: existingTargetSecret(
				freshTargetAnnotations(now.Add(2*time.Hour), caFileSource),
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
					TokenExpirationKey:  []byte(now.Add(2 * time.Hour).Format(time.RFC3339)),
				},
			),
			expectedError: "external managed kubeconfig cluster references certificate-authority file \"/etc/ssl/source-ca.crt\", which is not portable",
			validate:      assertNoSecretWrite,
		},
		{
			name:             "rejects source kubeconfig with client-certificate file",
			sourceKubeconfig: testKubeconfigWithAuthInfo(t, "https://managed.example.com", []byte("ca-1"), clientcmdapi.AuthInfo{ClientCertificate: "/etc/creds/client.crt", ClientKeyData: []byte("key")}),
			expectedError:    "external managed kubeconfig user references client-certificate file \"/etc/creds/client.crt\", which is not portable",
			validate:         assertNoSecretWrite,
		},
		{
			name:             "rejects source kubeconfig with client-key file",
			sourceKubeconfig: testKubeconfigWithAuthInfo(t, "https://managed.example.com", []byte("ca-1"), clientcmdapi.AuthInfo{ClientCertificateData: []byte("crt"), ClientKey: "/etc/creds/client.key"}),
			expectedError:    "external managed kubeconfig user references client-key file \"/etc/creds/client.key\", which is not portable",
			validate:         assertNoSecretWrite,
		},
		{
			name:             "rejects source kubeconfig with token file",
			sourceKubeconfig: testKubeconfigWithAuthInfo(t, "https://managed.example.com", []byte("ca-1"), clientcmdapi.AuthInfo{TokenFile: "/var/run/secrets/token"}),
			expectedError:    "external managed kubeconfig user references token file \"/var/run/secrets/token\", which is not portable",
			validate:         assertNoSecretWrite,
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
			validate:      assertNoSecretWrite,
		},
		{
			name:             "copies hub kubeconfig secret to hosting cluster",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				freshTargetAnnotations(now.Add(2*time.Hour), portable),
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
					TokenExpirationKey:  []byte(now.Add(2 * time.Hour).Format(time.RFC3339)),
				},
			),
			managedSecret: managedHubKubeConfigSecret([]byte("hub-kubeconfig-1")),
			mutate:        func(o *Provisioner) { o.HubKubeConfigSecret = DefaultHubKubeConfigSecret },
			validate: func(t *testing.T, hostingClient, managedClient *fakekube.Clientset) {
				secret, err := hostingClient.CoreV1().Secrets("addon-ns").Get(context.Background(), DefaultHubKubeConfigSecret, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, corev1.SecretTypeOpaque, secret.Type)
				assert.Equal(t, []byte("hub-kubeconfig-1"), secret.Data[KubeconfigSecretKey])
				assertNoAction(t, managedClient.Actions(), "create", "serviceaccounts/token")
			},
		},
		{
			name:             "uses existing hosted hub kubeconfig secret when managed secret is absent",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				freshTargetAnnotations(now.Add(2*time.Hour), portable),
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
					TokenExpirationKey:  []byte(now.Add(2 * time.Hour).Format(time.RFC3339)),
				},
			),
			existingHub: hostedHubKubeConfigSecret([]byte("hosted-hub-kubeconfig")),
			mutate:      func(o *Provisioner) { o.HubKubeConfigSecret = DefaultHubKubeConfigSecret },
			validate: func(t *testing.T, hostingClient, managedClient *fakekube.Clientset) {
				secret, err := hostingClient.CoreV1().Secrets("addon-ns").Get(context.Background(), DefaultHubKubeConfigSecret, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, []byte("hosted-hub-kubeconfig"), secret.Data[KubeconfigSecretKey])
				assertNoAction(t, hostingClient.Actions(), "update", "secrets")
				assertNoAction(t, managedClient.Actions(), "create", "serviceaccounts/token")
			},
		},
		{
			name:             "updates hosted hub kubeconfig secret when managed secret changes",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				freshTargetAnnotations(now.Add(2*time.Hour), portable),
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
					TokenExpirationKey:  []byte(now.Add(2 * time.Hour).Format(time.RFC3339)),
				},
			),
			existingHub:   hostedHubKubeConfigSecret([]byte("old-hub-kubeconfig")),
			managedSecret: managedHubKubeConfigSecret([]byte("new-hub-kubeconfig")),
			mutate:        func(o *Provisioner) { o.HubKubeConfigSecret = DefaultHubKubeConfigSecret },
			validate: func(t *testing.T, hostingClient, _ *fakekube.Clientset) {
				secret, err := hostingClient.CoreV1().Secrets("addon-ns").Get(context.Background(), DefaultHubKubeConfigSecret, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, []byte("new-hub-kubeconfig"), secret.Data[KubeconfigSecretKey])
				assertAction(t, hostingClient.Actions(), "update", "secrets")
			},
		},
		{
			name:             "rejects managed hub kubeconfig secret missing kubeconfig data",
			sourceKubeconfig: portable,
			existing: existingTargetSecret(
				freshTargetAnnotations(now.Add(2*time.Hour), portable),
				map[string][]byte{
					KubeconfigSecretKey: []byte("existing"),
					TokenExpirationKey:  []byte(now.Add(2 * time.Hour).Format(time.RFC3339)),
				},
			),
			managedSecret: managedHubKubeConfigSecret(nil),
			mutate:        func(o *Provisioner) { o.HubKubeConfigSecret = DefaultHubKubeConfigSecret },
			expectedError: "managed cluster hub kubeconfig secret addon-ns/managed-serviceaccount-hub-kubeconfig missing \"kubeconfig\" data",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var objs []runtime.Object
			if c.sourceKubeconfig != nil {
				objs = append(objs, newSourceSecret(c.sourceKubeconfig))
			}
			if c.existing != nil {
				objs = append(objs, c.existing)
			}
			if c.existingHub != nil {
				objs = append(objs, c.existingHub)
			}
			hostingClient := fakekube.NewSimpleClientset(objs...)
			var managedObjs []runtime.Object
			if c.managedSecret != nil {
				managedObjs = append(managedObjs, c.managedSecret)
			}
			managedClient := fakekube.NewSimpleClientset(managedObjs...)
			if c.stubToken != nil {
				c.stubToken(t, managedClient)
			}
			p := newTestProvisioner(hostingClient, managedClient, func(o *Provisioner) {
				o.Clock = clock.NewFakeClock(now)
				if c.mutate != nil {
					c.mutate(o)
				}
			})

			err := p.Sync(context.Background())

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
			name:   "empty target namespace",
			mutate: func(o *Provisioner) { o.TargetNamespace = "" },
		},
		{
			name:   "empty target secret",
			mutate: func(o *Provisioner) { o.TargetSecret = "" },
		},
		{
			name:   "negative token expiration seconds",
			mutate: func(o *Provisioner) { o.TokenExpirationSeconds = -1 },
		},
		{
			name:   "negative refresh before",
			mutate: func(o *Provisioner) { o.RefreshBefore = -time.Minute },
		},
		{
			name:   "refresh before not shorter than token lifetime",
			mutate: func(o *Provisioner) { o.TokenExpirationSeconds = 3600; o.RefreshBefore = time.Hour },
		},
		{
			name:   "target secret colliding with source",
			mutate: func(o *Provisioner) { o.TargetNamespace = "source-ns"; o.TargetSecret = "external-managed-kubeconfig" },
		},
		{
			name:   "target secret colliding with hosted hub kubeconfig secret",
			mutate: func(o *Provisioner) { o.TargetSecret = DefaultHubKubeConfigSecret },
		},
		{
			name: "external managed kubeconfig secret colliding with hosted hub kubeconfig secret",
			mutate: func(o *Provisioner) {
				o.SourceNamespace = "addon-ns"
				o.SourceSecret = DefaultHubKubeConfigSecret
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestProvisioner(fakekube.NewSimpleClientset(), fakekube.NewSimpleClientset(), c.mutate)
			assert.Error(t, p.Complete())
		})
	}
}

func TestCleanupIgnoresMissingTargetSecret(t *testing.T) {
	hostingClient := fakekube.NewSimpleClientset()
	p := newTestProvisioner(hostingClient, fakekube.NewSimpleClientset(), nil)

	err := p.Cleanup(context.Background())

	assert.NoError(t, err)
}

func TestCleanupDeletesExistingTargetSecret(t *testing.T) {
	hostingClient := fakekube.NewSimpleClientset(existingTargetSecret(nil, map[string][]byte{
		KubeconfigSecretKey: []byte("existing"),
		TokenExpirationKey:  []byte("2026-05-13T01:00:00Z"),
	}))
	p := newTestProvisioner(hostingClient, fakekube.NewSimpleClientset(), nil)

	err := p.Cleanup(context.Background())

	assert.NoError(t, err)
	_, err = hostingClient.CoreV1().Secrets("addon-ns").Get(context.Background(), "target-kubeconfig", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "expected target secret to be deleted, got error %v", err)
	assertAction(t, hostingClient.Actions(), "delete", "secrets")
}

func TestCleanupDeletesCopiedHubKubeConfigSecret(t *testing.T) {
	hostingClient := fakekube.NewSimpleClientset(
		existingTargetSecret(nil, map[string][]byte{
			KubeconfigSecretKey: []byte("existing"),
			TokenExpirationKey:  []byte("2026-05-13T01:00:00Z"),
		}),
		hostedHubKubeConfigSecret([]byte("hub-kubeconfig")),
	)
	p := newTestProvisioner(hostingClient, fakekube.NewSimpleClientset(), func(o *Provisioner) {
		o.HubKubeConfigSecret = DefaultHubKubeConfigSecret
	})

	err := p.Cleanup(context.Background())

	assert.NoError(t, err)
	_, err = hostingClient.CoreV1().Secrets("addon-ns").Get(context.Background(), "target-kubeconfig", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "expected target secret to be deleted, got error %v", err)
	_, err = hostingClient.CoreV1().Secrets("addon-ns").Get(context.Background(), DefaultHubKubeConfigSecret, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "expected copied hub kubeconfig secret to be deleted, got error %v", err)
}

func TestCompleteAppliesDefaults(t *testing.T) {
	p := &Provisioner{
		HostingClient:   fakekube.NewSimpleClientset(),
		TargetNamespace: "addon-ns",
		TargetSecret:    "target-kubeconfig",
	}

	assert.NoError(t, p.Complete())

	assert.Equal(t, DefaultExternalManagedKubeConfigSecret, p.SourceSecret)
	assert.Equal(t, "addon-ns", p.ManagedServiceAccountNamespace)
	assert.Equal(t, DefaultManagedServiceAccountName, p.ManagedServiceAccountName)
	assert.Equal(t, DefaultTokenExpirationSeconds, p.TokenExpirationSeconds)
	assert.Equal(t, DefaultRefreshBefore, p.RefreshBefore)
	assert.Equal(t, DefaultHubKubeConfigSecret, p.HubKubeConfigSecret)
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

func managedHubKubeConfigSecret(kubeconfig []byte) *corev1.Secret {
	secret := hostedHubKubeConfigSecret(kubeconfig)
	secret.Namespace = "addon-ns"
	return secret
}

func hostedHubKubeConfigSecret(kubeconfig []byte) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        DefaultHubKubeConfigSecret,
			Namespace:   "addon-ns",
			Labels:      map[string]string{"source": "managed"},
			Annotations: map[string]string{"rotation": "test"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{},
	}
	if kubeconfig != nil {
		secret.Data[KubeconfigSecretKey] = kubeconfig
	}
	return secret
}

// existingTargetSecret builds a pre-existing target secret at addon-ns/target-kubeconfig.
func existingTargetSecret(annotations map[string]string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "target-kubeconfig",
			Namespace:   "addon-ns",
			Annotations: annotations,
		},
		Data: data,
	}
}

// freshTargetAnnotations returns the annotations the provisioner stamps for the default
// managed serviceaccount identity, which mark the target secret as up to date.
func freshTargetAnnotations(expires time.Time, sourceKubeconfig []byte) map[string]string {
	return map[string]string{
		TokenExpirationAnnotation:                expires.Format(time.RFC3339),
		SourceKubeconfigHashAnnotation:           sourceKubeconfigHash(sourceKubeconfig),
		ManagedServiceAccountNamespaceAnnotation: "addon-ns",
		ManagedServiceAccountNameAnnotation:      "managed-serviceaccount",
		TokenExpirationSecondsAnnotation:         "3600",
	}
}

func getTargetSecret(t *testing.T, hostingClient *fakekube.Clientset) *corev1.Secret {
	t.Helper()
	secret, err := hostingClient.CoreV1().Secrets("addon-ns").Get(context.Background(), "target-kubeconfig", metav1.GetOptions{})
	assert.NoError(t, err)
	return secret
}

func loadTargetKubeconfig(t *testing.T, secret *corev1.Secret) *clientcmdapi.Config {
	t.Helper()
	kubeconfig, err := clientcmd.Load(secret.Data[KubeconfigSecretKey])
	assert.NoError(t, err)
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

func assertAction(t *testing.T, actions []clienttesting.Action, verb, resource string) {
	t.Helper()

	for _, action := range actions {
		if action.Matches(verb, resource) {
			return
		}
	}
	t.Fatalf("expected action %s %s in %#v", verb, resource, actions)
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
