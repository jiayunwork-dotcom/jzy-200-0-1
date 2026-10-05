# --- build stage ---
FROM golang:1.23-alpine AS build
WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG DRIVER=postgres
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/crackstation ./cmd/server

# --- runtime stage: slim image ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget && \
    addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=build /out/crackstation /app/crackstation
USER app
EXPOSE 8080
ENTRYPOINT ["/app/crackstation"]
