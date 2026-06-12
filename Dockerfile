FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS builder
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /go/src/github.com/open-cluster-management.io/managed-serviceaccount
COPY . .
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-$(go env GOHOSTARCH)} CGO_ENABLED=0 \
    GO_BUILD_FLAGS='-trimpath -a' GO_BUILD_LDFLAGS='-s -w' make build-bin

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
COPY --from=builder /go/src/github.com/open-cluster-management.io/managed-serviceaccount/bin/msa /
