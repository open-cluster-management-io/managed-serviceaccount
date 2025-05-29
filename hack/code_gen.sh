#!/usr/bin/env bash

REPO_DIR="$(cd "$(dirname ${BASH_SOURCE[0]})/.." ; pwd -P)"
API_PKG="open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
OUTPUT_PKG="open-cluster-management.io/managed-serviceaccount/pkg/generated"

set -o errexit
set -o nounset
set -o pipefail

set -x

GOBIN=${REPO_DIR}/bin

# Generate clientset
${GOBIN}/client-gen --go-header-file="${REPO_DIR}/hack/boilerplate.go.txt" \
    --clientset-name="versioned" \
    --input-base="open-cluster-management.io/managed-serviceaccount/apis" \
    --input="authentication/v1alpha1,authentication/v1beta1" \
    --output-base ./_output/gen \
    --output-package="${OUTPUT_PKG}/clientset"

# Generate listers
${GOBIN}/lister-gen --go-header-file="${REPO_DIR}/hack/boilerplate.go.txt" \
    --input-dirs=${API_PKG} \
    --output-base ./_output/gen \
    --output-package="${OUTPUT_PKG}/listers"

# Generate informers
${GOBIN}/informer-gen --go-header-file="${REPO_DIR}/hack/boilerplate.go.txt" \
    --input-dirs="${API_PKG}" \
    --versioned-clientset-package="${OUTPUT_PKG}/clientset/versioned" \
    --listers-package="${OUTPUT_PKG}/listers" \
    --output-base ./_output/gen \
    --output-package="${OUTPUT_PKG}/informers"

# Update generated code
rm -rf ${REPO_DIR}/pkg/generated
mv _output/gen/open-cluster-management.io/managed-serviceaccount/pkg/generated pkg/generated
rm -rf _output/gen

