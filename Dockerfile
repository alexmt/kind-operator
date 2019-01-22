# Build the manager binary
FROM golang:1.10.3 as builder

# Install kind
RUN go get sigs.k8s.io/kind

# Install docker
ENV DOCKER_VERSION=18.06.0
RUN curl -O https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_VERSION}-ce.tgz && \
  tar -xzf docker-${DOCKER_VERSION}-ce.tgz

# Copy in the go src
WORKDIR /go/src/github.com/argoproj/kind-operator
COPY pkg/    pkg/
COPY cmd/    cmd/
COPY vendor/ vendor/



# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager github.com/argoproj/kind-operator/cmd/manager

# Copy the controller-manager into a thin image
FROM ubuntu:latest
WORKDIR /
COPY --from=builder /go/src/github.com/argoproj/kind-operator/manager .
COPY --from=builder /go/bin/kind /usr/local/bin/kind
COPY --from=builder /go/docker/docker /usr/local/bin/docker

ENTRYPOINT ["/manager"]
