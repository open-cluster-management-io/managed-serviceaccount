FROM golang:1.21-bullseye AS builder
WORKDIR /go/src/github.com/open-cluster-management.io/managed-serviceaccount
COPY . .
RUN go env
RUN make build-manager
RUN make build-agent

FROM registry.access.redhat.com/ubi8/ubi-minimal:latest
COPY --from=builder /go/src/github.com/open-cluster-management.io/managed-serviceaccount/bin/manager /
COPY --from=builder /go/src/github.com/open-cluster-management.io/managed-serviceaccount/bin/agent /
