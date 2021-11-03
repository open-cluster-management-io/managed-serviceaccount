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
	"open-cluster-management.io/api/addon/v1alpha1"
	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/managed-serviceaccount/pkg/common"
)

var _ agent.AgentAddon = &managedServiceAccountAddonAgent{}

func NewManagedServiceAccountAddonAgent(c kubernetes.Interface) agent.AgentAddon {
	return &managedServiceAccountAddonAgent{
		nativeClient: c,
	}
}

type managedServiceAccountAddonAgent struct {
	nativeClient kubernetes.Interface
}

func (m *managedServiceAccountAddonAgent) Manifests(cluster *clusterv1.ManagedCluster, addon *v1alpha1.ManagedClusterAddOn) ([]runtime.Object, error) {
	namespace := addon.Spec.InstallNamespace
	return []runtime.Object{
		newAddonAgentDeployment(namespace),
	}, nil
}

func (m *managedServiceAccountAddonAgent) GetAgentAddonOptions() agent.AgentAddonOptions {
	return agent.AgentAddonOptions{
		AddonName: common.AddonName,
		Registration: &agent.RegistrationOption{
			CSRConfigurations: agent.KubeClientSignerConfigurations(common.AddonName, common.AgentName),
			CSRApproveCheck:   agent.ApprovalAllCSRs,
			PermissionConfig:  m.setupPermission,
		},
	}
}

func (m *managedServiceAccountAddonAgent) setupPermission(cluster *clusterv1.ManagedCluster, addon *addonv1alpha1.ManagedClusterAddOn) error {
	namespace := cluster.Name
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "open-cluster-management:addon:agent:managed-serviceaccount",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
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
	agentUser := "system:open-cluster-management:cluster:" + cluster.Name + ":addon:managed-serviceaccount:agent:addon-agent"
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "open-cluster-management:addon:agent:managed-serviceaccount",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "Role",
			Name: "open-cluster-management:addon:agent:managed-serviceaccount",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: rbacv1.UserKind,
				Name: agentUser,
			},
		},
	}
	if _, err := m.nativeClient.RbacV1().Roles(namespace).
		Create(context.TODO(), role, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	if _, err := m.nativeClient.RbacV1().RoleBindings(namespace).
		Create(context.TODO(), roleBinding, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

func newAddonAgentDeployment(namespace string) *appsv1.Deployment {
	const secretName = "managed-serviceaccount-hub-kubeconfig"
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dummy",
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"foo": "bar",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "foo",
							Image: "nginx",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "hub-kubeconfig",
									ReadOnly:  true,
									MountPath: "/etc/hub/",
								},
							},
						},
					},
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
}
