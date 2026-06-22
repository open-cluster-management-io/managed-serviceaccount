#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
kubeconfig_dir=${KUBECONFIG_DIR:-"${repo_root}/_output/e2e/kubeconfigs"}
hub_cluster=${HUB_CLUSTER_NAME:-chart-testing}
hosting_cluster=${HOSTING_CLUSTER_NAME:-hosting}
spoke_cluster=${SPOKE_CLUSTER_NAME:-spoke}
image=${E2E_IMAGE:-quay.io/open-cluster-management/managed-serviceaccount:latest}

mkdir -p "${kubeconfig_dir}"

kind create cluster --name "${hosting_cluster}"
kind create cluster --name "${spoke_cluster}"

# Use kind-network IPs so the kubeconfigs work both on the runner and from
# workloads in the other kind clusters without modifying the runner's hosts.
write_kubeconfig() {
	local cluster_name=$1
	local output=$2
	local control_plane_ip

	kind get kubeconfig --name "${cluster_name}" --internal >"${output}"
	control_plane_ip=$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' \
		"${cluster_name}-control-plane")
	kubectl --kubeconfig="${output}" config set-cluster "kind-${cluster_name}" \
		--server="https://${control_plane_ip}:6443"
}

write_kubeconfig "${hub_cluster}" "${kubeconfig_dir}/hub.kubeconfig"
write_kubeconfig "${hosting_cluster}" "${kubeconfig_dir}/${hosting_cluster}.kubeconfig"
write_kubeconfig "${spoke_cluster}" "${kubeconfig_dir}/${spoke_cluster}.kubeconfig"

# The image was built by the preceding e2e phase, where the agent runs on the
# managed cluster. The hub already has it; the hosting cluster needs it for the
# hosted agent and kubeconfig provisioner.
kind load docker-image "${image}" --name "${hosting_cluster}"

export KUBECONFIG="${kubeconfig_dir}/hub.kubeconfig"

# The hosted phase must not depend on the managed-cluster cleanup suite having
# disabled automatic installation. Otherwise a failed first phase can race the
# hosted setup by creating a Default-mode addon on the newly joined clusters.
kubectl patch clustermanagementaddon managed-serviceaccount --type=merge \
	--patch '{"spec":{"installStrategy":{"type":"Manual","placements":[]}}}'

hub_info=$(clusteradm get token -o json)
hub_token=$(jq -r '."hub-token"' <<<"${hub_info}")
hub_apiserver=$(jq -r '."hub-apiserver"' <<<"${hub_info}")

join_cluster() {
	local kubeconfig=$1
	local cluster_name=$2
	shift 2

	KUBECONFIG="${kubeconfig}" clusteradm join \
		--hub-token "${hub_token}" \
		--hub-apiserver "${hub_apiserver}" \
		--cluster-name "${cluster_name}" \
		--wait \
		--force-internal-endpoint-lookup \
		"$@"
}

join_cluster "${kubeconfig_dir}/${hosting_cluster}.kubeconfig" "${hosting_cluster}"
clusteradm accept --clusters "${hosting_cluster}" --wait 30
kubectl wait --for=condition=ManagedClusterConditionAvailable \
	"managedcluster/${hosting_cluster}" --timeout=2m

# Prevent any incorrectly placed addon workload from becoming Ready on the
# managed cluster. A final assertion also rejects even Pending addon pods.
kubectl --kubeconfig="${kubeconfig_dir}/${spoke_cluster}.kubeconfig" \
	wait --for=condition=Ready nodes --all --timeout=2m
kubectl --kubeconfig="${kubeconfig_dir}/${spoke_cluster}.kubeconfig" taint nodes --all \
	hosted-e2e.open-cluster-management.io/no-addon-workloads=true:NoSchedule --overwrite

join_cluster \
	"${kubeconfig_dir}/${hosting_cluster}.kubeconfig" \
	"${spoke_cluster}" \
	--mode hosted \
	--managed-cluster-kubeconfig "${kubeconfig_dir}/${spoke_cluster}.kubeconfig"
clusteradm accept --clusters "${spoke_cluster}" --wait 30
kubectl wait --for=condition=ManagedClusterConditionAvailable \
	"managedcluster/${spoke_cluster}" --timeout=2m

# The e2e BeforeSuite creates the external kubeconfig source namespace and
# Secret before it creates or converts the ManagedClusterAddOn to Hosted mode,
# avoiding an expected first ManifestWork failure.
