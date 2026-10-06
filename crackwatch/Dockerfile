# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.23-alpine AS build
WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG CGO_ENABLED=0
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/crackwatch ./cmd/crackwatch

# ---- runtime stage: minimal image, no shell, non-root ----
FROM scratch
# CA certificates are only needed if PostgreSQL is reached over TLS; copy
# them from the alpine cert bundle rather than shipping a package manager.
COPY --from=alpine:3.20 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/crackwatch /crackwatch

USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/crackwatch"]
