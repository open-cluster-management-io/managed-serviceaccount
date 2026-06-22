#!/usr/bin/env bash

set -o nounset
set -o pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
kubeconfig_dir=${KUBECONFIG_DIR:-"${repo_root}/_output/e2e/kubeconfigs"}
hosting_cluster=${HOSTING_CLUSTER_NAME:-hosting}
spoke_cluster=${SPOKE_CLUSTER_NAME:-spoke}
hub_args=()

if [[ -f "${kubeconfig_dir}/hub.kubeconfig" ]]; then
	hub_args=(--kubeconfig="${kubeconfig_dir}/hub.kubeconfig")
fi

# The agent and the provisioner both carry an addon-agent label, so their
# namespaces do not have to be known up front.
dump_agent_logs() {
	local namespace
	for namespace in $(kubectl "$@" get pods -A -l addon-agent \
		-o jsonpath='{range .items[*]}{.metadata.namespace}{"\n"}{end}' | sort -u); do
		kubectl "$@" -n "${namespace}" \
			logs -l addon-agent --all-containers --tail=-1 --prefix || true
	done
}

kubectl "${hub_args[@]}" get clustermanagementaddon managed-serviceaccount -o yaml || true
kubectl "${hub_args[@]}" get managedclusteraddon -A -o yaml || true
kubectl "${hub_args[@]}" get addondeploymentconfig -A -o yaml || true
kubectl "${hub_args[@]}" get manifestwork -A -o yaml || true
kubectl "${hub_args[@]}" get deploy,pod,serviceaccount,role,rolebinding,lease -A -o wide || true
kubectl "${hub_args[@]}" get events -A --sort-by=.lastTimestamp || true
kubectl "${hub_args[@]}" -n open-cluster-management-addon \
	logs deploy/managed-serviceaccount-addon-manager --all-containers --tail=-1 --prefix || true
dump_agent_logs "${hub_args[@]}"

for cluster in "${hosting_cluster}" "${spoke_cluster}"; do
	kubeconfig="${kubeconfig_dir}/${cluster}.kubeconfig"
	if [[ ! -f "${kubeconfig}" ]]; then
		continue
	fi

	echo "=== ${cluster} resources ==="
	kubectl --kubeconfig="${kubeconfig}" \
		get deploy,pod,serviceaccount,role,rolebinding,secret,lease -A -o wide || true
	kubectl --kubeconfig="${kubeconfig}" \
		get events -A --sort-by=.lastTimestamp || true
	dump_agent_logs --kubeconfig="${kubeconfig}"
done
