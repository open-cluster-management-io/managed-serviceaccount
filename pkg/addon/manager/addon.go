package manager

import (
	"context"
	"embed"
	"encoding/base64"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/pointer"
	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"

	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

//go:embed manifests/templates
var FS embed.FS

func GetDefaultValues(image string, imagePullSecret *corev1.Secret) addonfactory.GetValuesFunc {
	return func(cluster *clusterv1.ManagedCluster, addon *addonv1alpha1.ManagedClusterAddOn) (addonfactory.Values, error) {
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
		CSRConfigurations: agent.KubeClientSignerConfigurations(common.AddonName, common.AgentName),
		CSRApproveCheck:   agent.ApprovalAllCSRs,
		PermissionConfig:  setupPermission(nativeClient),
	}
}

func setupPermission(nativeClient kubernetes.Interface) agent.PermissionConfigFunc {
	return func(cluster *clusterv1.ManagedCluster, addon *addonv1alpha1.ManagedClusterAddOn) error {
		namespace := cluster.Name
		agentUser := "system:open-cluster-management:cluster:" + cluster.Name + ":addon:managed-serviceaccount:agent:addon-agent"
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "managed-serviceaccount-addon-agent",
				Namespace: namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         "addon.open-cluster-management.io/v1alpha1",
						Kind:               "ManagedClusterAddOn",
						UID:                addon.UID,
						Name:               addon.Name,
						BlockOwnerDeletion: pointer.Bool(true),
					},
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Verbs:     []string{"get", "list", "create", "update", "patch"},
					Resources: []string{"events"},
				},
				{
					APIGroups: []string{""},
					Verbs:     []string{"*"},
					Resources: []string{"secrets", "configmaps"},
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
				{
					APIGroups: []string{"coordination.k8s.io"},
					Verbs:     []string{"*"},
					Resources: []string{"leases"},
				},
			},
		}
		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "managed-serviceaccount-addon-agent",
				Namespace: namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         "addon.open-cluster-management.io/v1alpha1",
						Kind:               "ManagedClusterAddOn",
						UID:                addon.UID,
						Name:               addon.Name,
						BlockOwnerDeletion: pointer.Bool(true),
					},
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind: "Role",
				Name: "managed-serviceaccount-addon-agent",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: rbacv1.UserKind,
					Name: agentUser,
				},
			},
		}

		if _, err := nativeClient.RbacV1().Roles(namespace).Create(
			context.TODO(),
			role,
			metav1.CreateOptions{}); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return err
			}
		}
		if _, err := nativeClient.RbacV1().RoleBindings(namespace).Create(
			context.TODO(),
			roleBinding,
			metav1.CreateOptions{}); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return err
			}
		}
		return nil
	}
}
