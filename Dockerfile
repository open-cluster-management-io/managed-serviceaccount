FROM golang:1.22-bullseye AS builder
WORKDIR /go/src/github.com/open-cluster-management.io/managed-serviceaccount
COPY . .
RUN go env
RUN make build-bin

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
COPY --from=builder /go/src/github.com/open-cluster-management.io/managed-serviceaccount/bin/msa /
