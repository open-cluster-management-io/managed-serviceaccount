package manager

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/pointer"
	"open-cluster-management.io/addon-framework/pkg/agent"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"

	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

var _ agent.AgentAddon = &managedServiceAccountAddonAgent{}

const agentImagePullSecretName = "open-cluster-management-image-pull-credentials"

func NewManagedServiceAccountAddonAgent(
	c kubernetes.Interface,
	imageName string,
	agentInstallAllStrategy bool,
	agentImagePullSecret *corev1.Secret,
) agent.AgentAddon {
	var agentInstallStrategy *agent.InstallStrategy
	agentInstallStrategy = nil
	if agentInstallAllStrategy {
		agentInstallStrategy = agent.InstallAllStrategy(common.AddonAgentInstallNamespace)
	}

	return &managedServiceAccountAddonAgent{
		nativeClient:         c,
		imageName:            imageName,
		agentInstallStrategy: agentInstallStrategy,
		agentImagePullSecret: agentImagePullSecret,
	}
}

type managedServiceAccountAddonAgent struct {
	nativeClient         kubernetes.Interface
	imageName            string
	agentInstallStrategy *agent.InstallStrategy
	agentImagePullSecret *corev1.Secret
}

func (m *managedServiceAccountAddonAgent) Manifests(cluster *clusterv1.ManagedCluster, addon *addonv1alpha1.ManagedClusterAddOn) ([]runtime.Object, error) {
	namespace := addon.Spec.InstallNamespace
	manifests := []runtime.Object{
		newNamespace(namespace),
		newServiceAccount(namespace),
		newAddonAgentClusterRole(namespace),
		newAddonAgentClusterRoleBinding(namespace),
		newAddonAgentRole(namespace),
		newAddonAgentRoleBinding(namespace),
		newAddonAgentDeployment(cluster.Name, namespace, m.imageName, m.agentImagePullSecret),
	}
	if m.agentImagePullSecret != nil {
		manifests = append(manifests, newAddonAgentImagePullSecret(m.agentImagePullSecret, namespace))
	}

	return manifests, nil
}

func (m *managedServiceAccountAddonAgent) GetAgentAddonOptions() agent.AgentAddonOptions {
	return agent.AgentAddonOptions{
		AddonName: common.AddonName,
		Registration: &agent.RegistrationOption{
			CSRConfigurations: agent.KubeClientSignerConfigurations(common.AddonName, common.AgentName),
			CSRApproveCheck:   agent.ApprovalAllCSRs,
			PermissionConfig:  m.setupPermission,
		},
		InstallStrategy: m.agentInstallStrategy,
	}
}

func (m *managedServiceAccountAddonAgent) setupPermission(cluster *clusterv1.ManagedCluster, addon *addonv1alpha1.ManagedClusterAddOn) error {
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

	if _, err := m.nativeClient.RbacV1().Roles(namespace).Create(
		context.TODO(),
		role,
		metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	if _, err := m.nativeClient.RbacV1().RoleBindings(namespace).Create(
		context.TODO(),
		roleBinding,
		metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

func newNamespace(targetNamespace string) *corev1.Namespace {
	return &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Namespace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: targetNamespace,
		},
	}
}

func newServiceAccount(namespace string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "managed-serviceaccount",
		},
	}
}

func newAddonAgentClusterRole(namespace string) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "open-cluster-management:managed-serviceaccount:addon-agent",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"authentication.k8s.io"},
				Resources: []string{"tokenreviews"},
				Verbs:     []string{"create"},
			},
		},
	}
}

func newAddonAgentRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "open-cluster-management:managed-serviceaccount:addon-agent",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Verbs:     []string{"get", "create", "update", "patch"},
				Resources: []string{"configmaps"},
			},
			{
				APIGroups: []string{"coordination.k8s.io"},
				Verbs:     []string{"get", "create", "update", "patch"},
				Resources: []string{"leases"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts", "serviceaccounts/token"},
				Verbs:     []string{"get", "watch", "list", "create", "delete"},
			},
			{
				APIGroups: []string{"authentication.k8s.io"},
				Resources: []string{"tokenrequests"},
				Verbs:     []string{"create"},
			},
		},
	}
}

func newAddonAgentRoleBinding(namespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "open-cluster-management:managed-serviceaccount:addon-agent",
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "Role",
			Name: "open-cluster-management:managed-serviceaccount:addon-agent",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      "managed-serviceaccount",
				Namespace: namespace,
			},
		},
	}
}
func newAddonAgentClusterRoleBinding(namespace string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "open-cluster-management:managed-serviceaccount:addon-agent",
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "ClusterRole",
			Name: "open-cluster-management:managed-serviceaccount:addon-agent",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      "managed-serviceaccount",
				Namespace: namespace,
			},
		},
	}
}

func newAddonAgentImagePullSecret(agentImagePullSecret *corev1.Secret, targetNamespace string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentImagePullSecretName,
			Namespace: targetNamespace,
		},
		Data: agentImagePullSecret.Data,
		Type: corev1.SecretTypeDockerConfigJson,
	}
}

func newAddonAgentDeployment(clusterName string, namespace string, imageName string, agentImagePullSecret *corev1.Secret) *appsv1.Deployment {
	const secretName = "managed-serviceaccount-hub-kubeconfig"
	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "managed-serviceaccount-addon-agent",
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"addon-agent": "managed-serviceaccount",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"addon-agent": "managed-serviceaccount",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "addon-agent",
							Image:           imageName,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Command: []string{
								"/agent",
							},
							Args: []string{
								"--leader-elect=true",
								"--cluster-name=" + clusterName,
								"--kubeconfig=/etc/hub/kubeconfig",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "hub-kubeconfig",
									ReadOnly:  true,
									MountPath: "/etc/hub/",
								},
							},
						},
					},
					ServiceAccountName: "managed-serviceaccount",
					Volumes: []corev1.Volume{
						{
							Name: "hub-kubeconfig",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: secretName,
								},
							},
						},
					},
				},
			},
		},
	}
	if agentImagePullSecret != nil {
		deployment.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{Name: agentImagePullSecretName},
		}
	}
	return deployment
}
