package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
			name: "default mode watches only the hub kubeconfig",
			want: []string{"/etc/hub/kubeconfig"},
		},
		{
			name:            "hosted mode also watches the rotating managed kubeconfig",
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
		name      string
		opts      AgentOptions
		wantMode  string
		wantError string
	}{
		{
			name:     "defaults empty install mode",
			opts:     AgentOptions{},
			wantMode: addonconstants.InstallModeDefault,
		},
		{
			name:     "accepts default mode without spoke kubeconfig",
			opts:     AgentOptions{InstallMode: addonconstants.InstallModeDefault},
			wantMode: addonconstants.InstallModeDefault,
		},
		{
			name:     "accepts hosted mode with spoke kubeconfig",
			opts:     AgentOptions{InstallMode: addonconstants.InstallModeHosted, SpokeKubeconfig: "/etc/managed/kubeconfig"},
			wantMode: addonconstants.InstallModeHosted,
		},
		{
			name:      "rejects hosted mode without spoke kubeconfig",
			opts:      AgentOptions{InstallMode: addonconstants.InstallModeHosted},
			wantError: "--spoke-kubeconfig is required when --install-mode=Hosted",
		},
		{
			name:      "rejects invalid install mode",
			opts:      AgentOptions{InstallMode: "Detached"},
			wantError: "unsupported --install-mode \"Detached\", must be \"Default\" or \"Hosted\"",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.opts.validateInstallMode()
			if c.wantError != "" {
				assert.EqualError(t, err, c.wantError)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, c.wantMode, c.opts.InstallMode)
		})
	}
}
