package manager

import (
	"context"
	"embed"
	"encoding/base64"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"open-cluster-management.io/addon-framework/pkg/addonfactory"
	"open-cluster-management.io/addon-framework/pkg/agent"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

//go:embed manifests/templates
var FS embed.FS

var serviceMonitorGroupVersion = schema.GroupVersion{
	Group:   "monitoring.coreos.com",
	Version: "v1",
}

func NewAgentAddonFactory(addonName string, fs embed.FS, dir string) *addonfactory.AgentAddonFactory {
	scheme := runtime.NewScheme()
	addServiceMonitorToScheme(scheme)
	return addonfactory.NewAgentAddonFactory(addonName, fs, dir).WithScheme(scheme)
}

func addServiceMonitorToScheme(scheme *runtime.Scheme) {
	scheme.AddKnownTypeWithName(
		serviceMonitorGroupVersion.WithKind("ServiceMonitor"),
		&unstructured.Unstructured{},
	)
	scheme.AddKnownTypeWithName(
		serviceMonitorGroupVersion.WithKind("ServiceMonitorList"),
		&unstructured.UnstructuredList{},
	)
	metav1.AddToGroupVersion(scheme, serviceMonitorGroupVersion)
}

func GetDefaultValues(image string, imagePullSecret *corev1.Secret) addonfactory.GetValuesFunc {
	return func(cluster *clusterv1.ManagedCluster, addon *addonv1alpha1.ManagedClusterAddOn) (addonfactory.Values, error) {
		manifestConfig := struct {
			ClusterName                string
			Image                      string
			ImagePullSecretData        string
			AgentServiceMonitorEnabled string
			AgentServiceMonitorLabels  map[string]string
		}{
			ClusterName:                cluster.Name,
			Image:                      image,
			AgentServiceMonitorEnabled: "false",
			AgentServiceMonitorLabels:  map[string]string{},
		}

		if imagePullSecret != nil {
			manifestConfig.ImagePullSecretData = base64.StdEncoding.EncodeToString(imagePullSecret.Data[corev1.DockerConfigJsonKey])
		}

		return addonfactory.StructToValues(manifestConfig), nil
	}
}

func ToAddOnDeploymentConfigValues(config addonv1alpha1.AddOnDeploymentConfig) (addonfactory.Values, error) {
	values, err := addonfactory.ToAddOnDeploymentConfigValues(config)
	if err != nil {
		return nil, err
	}

	if raw, ok := values["AgentServiceMonitorLabels"].(string); ok {
		values["AgentServiceMonitorLabels"] = parseServiceMonitorLabels(raw)
	}

	return values, nil
}

func parseServiceMonitorLabels(raw string) map[string]string {
	labels := map[string]string{}
	for _, item := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(item, "=")
		key = strings.TrimSpace(key)
		if !ok || len(key) == 0 {
			continue
		}
		labels[key] = strings.TrimSpace(value)
	}
	return labels
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
						BlockOwnerDeletion: ptr.To(true),
					},
				},
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
						BlockOwnerDeletion: ptr.To(true),
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
