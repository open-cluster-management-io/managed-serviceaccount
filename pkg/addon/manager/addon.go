package manager

import (
	"context"
	"embed"
	"encoding/base64"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
	addonutils "open-cluster-management.io/addon-framework/pkg/utils"
	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

const permissionName = "managed-serviceaccount-addon-agent"

//go:embed manifests/templates
var FS embed.FS

func GetDefaultValues(image string, imagePullSecret *corev1.Secret) addonfactory.GetValuesFunc {
	return func(cluster *clusterv1.ManagedCluster, addon *addonv1beta1.ManagedClusterAddOn) (addonfactory.Values, error) {
		manifestConfig := struct {
			ClusterName         string
			Image               string
			ImagePullSecretData string
		}{
			ClusterName: cluster.Name,
			Image:       image,
		}

		if imagePullSecret != nil {
			manifestConfig.ImagePullSecretData = base64.StdEncoding.EncodeToString(imagePullSecret.Data[corev1.DockerConfigJsonKey])
		}

		return addonfactory.StructToValues(manifestConfig), nil
	}
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
