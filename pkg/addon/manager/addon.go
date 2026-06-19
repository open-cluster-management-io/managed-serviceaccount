package manager

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"

	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
	addonutils "open-cluster-management.io/addon-framework/pkg/utils"
	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const permissionName = "managed-serviceaccount-addon-agent"

//go:embed all:manifests
var FS embed.FS

const (
	prometheusEnabledVariableName              = "prometheusEnabled"
	prometheusServiceMonitorLabelsVariableName = "prometheusServiceMonitorLabels"
)

type prometheusValues struct {
	Enabled        bool
	ServiceMonitor struct {
		Labels map[string]string
	}
}

var serviceMonitorGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "ServiceMonitor",
}

// NewAgentScheme returns a scheme that registers ServiceMonitor as an unstructured type so
// the agent's ServiceMonitor manifest can be decoded without depending on the prometheus-operator
// API types.
func NewAgentScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(serviceMonitorGVK, &unstructured.Unstructured{})
	return s
}

func GetDefaultValues(image string, imagePullSecret *corev1.Secret) addonfactory.GetValuesFunc {
	return func(_ *clusterv1.ManagedCluster, _ *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
		values := addonfactory.Values{
			"Image": image,
		}

		if imagePullSecret != nil {
			dockerConfig, ok := imagePullSecret.Data[corev1.DockerConfigJsonKey]
			if !ok || len(dockerConfig) == 0 {
				return nil, fmt.Errorf(
					"image pull secret %s/%s missing %q",
					imagePullSecret.Namespace,
					imagePullSecret.Name,
					corev1.DockerConfigJsonKey,
				)
			}
			values["imagePullSecretData"] = base64.StdEncoding.EncodeToString(dockerConfig)
		}

		return values, nil
	}
}

// ToAddOnPrometheusValues maps the Prometheus customized variables of an
// AddOnDeploymentConfig into the nested Prometheus values consumed by the agent manifests.
func ToAddOnPrometheusValues(config addonv1beta1.AddOnDeploymentConfig) (addonfactory.Values, error) {
	var prometheus prometheusValues
	for _, variable := range config.Spec.CustomizedVariables {
		switch variable.Name {
		case prometheusEnabledVariableName:
			enabled, err := strconv.ParseBool(variable.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid %s: %w", prometheusEnabledVariableName, err)
			}
			prometheus.Enabled = enabled
		case prometheusServiceMonitorLabelsVariableName:
			if err := json.Unmarshal([]byte(variable.Value), &prometheus.ServiceMonitor.Labels); err != nil {
				return nil, fmt.Errorf("invalid %s: %w", prometheusServiceMonitorLabelsVariableName, err)
			}
		}
	}

	if !prometheus.Enabled && len(prometheus.ServiceMonitor.Labels) == 0 {
		return nil, nil
	}

	return addonfactory.Values{"Prometheus": addonfactory.StructToValues(prometheus)}, nil
}

func NewRegistrationOption(nativeClient kubernetes.Interface) *agent.RegistrationOption {
	return &agent.RegistrationOption{
		Configurations:   agent.KubeClientSignerConfigurations(common.AddonName, common.AgentName),
		CSRApproveCheck:  approveAllCSRs,
		PermissionConfig: setupPermission(nativeClient),
	}
}

func approveAllCSRs(context.Context, *clusterv1.ManagedCluster, *addonv1beta1.ManagedClusterAddOn, *certificatesv1.CertificateSigningRequest) bool {
	return true
}

func setupPermission(nativeClient kubernetes.Interface) agent.PermissionConfigFunc {
	return func(ctx context.Context, cluster *clusterv1.ManagedCluster, addon *addonv1beta1.ManagedClusterAddOn) error {
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name: permissionName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Verbs:     []string{"get", "list", "watch", "create", "update"},
					Resources: []string{"secrets"},
				},
				{
					APIGroups: []string{"authentication.open-cluster-management.io"},
					Verbs:     []string{"get", "list", "watch"},
					Resources: []string{"managedserviceaccounts"},
				},
				{
					APIGroups: []string{"authentication.open-cluster-management.io"},
					Verbs:     []string{"get", "update", "patch"},
					Resources: []string{"managedserviceaccounts/status"},
				},
			},
		}

		return addonutils.NewRBACPermissionConfigBuilder(nativeClient).
			BindKubeClientRole(role).
			Build()(ctx, cluster, addon)
	}
}
