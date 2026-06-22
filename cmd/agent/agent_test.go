package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	addonconstants "open-cluster-management.io/addon-framework/pkg/addonmanager/constants"
)

func TestConfigCheckerPaths(t *testing.T) {
	t.Setenv("HUB_KUBECONFIG", "/etc/hub/kubeconfig")

	cases := []struct {
		name            string
		spokeKubeconfig string
		want            []string
	}{
		{
			name: "agent on managed cluster watches only the hub kubeconfig",
			want: []string{"/etc/hub/kubeconfig"},
		},
		{
			name:            "agent on hosting cluster also watches the rotating managed kubeconfig",
			spokeKubeconfig: "/etc/managed/kubeconfig",
			want:            []string{"/etc/hub/kubeconfig", "/etc/managed/kubeconfig"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := &AgentOptions{SpokeKubeconfig: c.spokeKubeconfig}
			assert.Equal(t, c.want, opts.configCheckerPaths())
		})
	}
}

func TestValidateInstallMode(t *testing.T) {
	cases := []struct {
		name          string
		opts          AgentOptions
		expectedError string
	}{
		{
			name: "accepts agent on managed cluster without spoke kubeconfig",
			opts: AgentOptions{InstallMode: addonconstants.InstallModeDefault},
		},
		{
			name:          "rejects empty install mode",
			opts:          AgentOptions{},
			expectedError: `unsupported --install-mode "", must be "Default" or "Hosted"`,
		},
		{
			name: "accepts agent on hosting cluster with spoke kubeconfig",
			opts: AgentOptions{InstallMode: addonconstants.InstallModeHosted, SpokeKubeconfig: "/etc/managed/kubeconfig"},
		},
		{
			name:          "rejects agent on hosting cluster without spoke kubeconfig",
			opts:          AgentOptions{InstallMode: addonconstants.InstallModeHosted},
			expectedError: "--spoke-kubeconfig is required when --install-mode=Hosted",
		},
		{
			name:          "rejects invalid install mode",
			opts:          AgentOptions{InstallMode: "Detached"},
			expectedError: `unsupported --install-mode "Detached", must be "Default" or "Hosted"`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.opts.validateInstallMode()
			if len(c.expectedError) > 0 {
				assert.EqualError(t, err, c.expectedError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLeaseClientConfig(t *testing.T) {
	spokeCfg := &rest.Config{Host: "https://spoke.example.com"}
	inClusterCfg := &rest.Config{Host: "https://hosting.example.com"}

	cases := []struct {
		name        string
		installMode string
		expected    *rest.Config
	}{
		{
			name:        "default mode writes the lease to the spoke cluster",
			installMode: addonconstants.InstallModeDefault,
			expected:    spokeCfg,
		},
		{
			name:        "hosted mode writes the lease to the cluster the agent runs on",
			installMode: addonconstants.InstallModeHosted,
			expected:    inClusterCfg,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := leaseClientConfig(c.installMode, spokeCfg, func() (*rest.Config, error) {
				return inClusterCfg, nil
			})
			assert.NoError(t, err)
			assert.Same(t, c.expected, cfg)
		})
	}
}

func TestLeaseHealthCheckFuncs(t *testing.T) {
	discoveryClient := &fakediscovery.FakeDiscovery{Fake: &clienttesting.Fake{}}

	assert.Empty(t, leaseHealthCheckFuncs(addonconstants.InstallModeDefault, discoveryClient))
	assert.Len(t, leaseHealthCheckFuncs(addonconstants.InstallModeHosted, discoveryClient), 1)
}

func TestManagedHealthClientConfig(t *testing.T) {
	spokeCfg := &rest.Config{Host: "https://managed.example", Timeout: time.Minute}

	assert.Nil(t, managedHealthClientConfig(addonconstants.InstallModeDefault, spokeCfg))
	hostedCfg := managedHealthClientConfig(addonconstants.InstallModeHosted, spokeCfg)
	assert.NotSame(t, spokeCfg, hostedCfg)
	assert.Equal(t, managedHealthRequestTimeout, hostedCfg.Timeout)
	assert.Equal(t, time.Minute, spokeCfg.Timeout)
}
