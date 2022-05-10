FROM golang:1.17 AS builder
WORKDIR /go/src/github.com/open-cluster-management.io/managed-serviceaccount
COPY . .
ENV GO_PACKAGE github.com/open-cluster-management.io/managed-serviceaccount
ENV GOFLAGS ""
RUN go env
RUN go build -a -o manager cmd/manager/main.go
RUN go build -a -o agent cmd/agent/main.go

FROM registry.access.redhat.com/ubi8/ubi-minimal:latest
COPY --from=builder /go/src/github.com/open-cluster-management.io/managed-serviceaccount/manager /
COPY --from=builder /go/src/github.com/open-cluster-management.io/managed-serviceaccount/agent /
