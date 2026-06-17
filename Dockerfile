FROM golang:1.26.1-alpine AS base
WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata git iputils
COPY go.mod ./
RUN go mod download

FROM base AS development
COPY . .
EXPOSE 8080
CMD ["go", "run", "./cmd/webguard-instance", "serve"]

FROM base AS builder
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w" -o /out/webguard-instance ./cmd/webguard-instance

FROM alpine:3.23 AS production
RUN apk add --no-cache ca-certificates tzdata wget iputils
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/webguard-instance /usr/local/bin/webguard-instance
USER app
EXPOSE 8080
ENTRYPOINT ["webguard-instance"]
CMD ["serve"]
