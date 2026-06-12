package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigCheckerPaths(t *testing.T) {
	t.Setenv("HUB_KUBECONFIG", "/etc/hub/kubeconfig")

	cases := []struct {
		name            string
		spokeKubeconfig string
		want            []string
	}{
		{
			name: "default watches only the hub kubeconfig",
			want: []string{"/etc/hub/kubeconfig"},
		},
		{
			name:            "spoke kubeconfig also watched when provided",
			spokeKubeconfig: "/etc/spoke/kubeconfig",
			want:            []string{"/etc/hub/kubeconfig", "/etc/spoke/kubeconfig"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &AgentOptions{SpokeKubeconfig: tc.spokeKubeconfig}

			assert.Equal(t, tc.want, opts.configCheckerPaths())
		})
	}
}
