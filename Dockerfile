# Build the manager binary
FROM golang:1.27@sha256:ec47ea3725fea0cc88f6de94919aef19f40f12fc1ec0e2d1b3426dbf15ecca04 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=""

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/controller/ internal/controller/
COPY internal/hostnamepolicy/ internal/hostnamepolicy/
COPY internal/accesspolicy/ internal/accesspolicy/
COPY internal/accessgate/ internal/accessgate/
COPY internal/webuiguard/ internal/webuiguard/
COPY cmd/access-proxy/ cmd/access-proxy/
COPY internal/enrollment/ internal/enrollment/
COPY internal/enrollmentkube/ internal/enrollmentkube/
COPY cmd/enrollment/ cmd/enrollment/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -ldflags "-X main.version=${VERSION}" -o manager cmd/main.go
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o access-proxy ./cmd/access-proxy
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o chat-ui-enrollment ./cmd/enrollment

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot@sha256:e8a4044e0b4ae4257efa45fc026c0bc30ad320d43bd4c1a7d5271bd241e386d0
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/access-proxy .
COPY --from=builder /workspace/chat-ui-enrollment .
USER 65532:65532

ENTRYPOINT ["/manager"]

