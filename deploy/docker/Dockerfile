# syntax=docker/dockerfile:1
#
# Production image for the gateway. Build from the repository root:
#
#   docker build -f deploy/docker/Dockerfile -t webhook-gateway .
#
# (deploy/docker/docker-compose.yml already does this for you.)

# --- build ---------------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first: a code-only change reuses this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off produces a static binary — pgx is pure Go, and both the migrations
# and the provider catalog are go:embed'ed, so the runtime image needs no
# files besides the binary itself. -buildvcs=false because .dockerignore
# keeps .git out of the context.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -buildvcs=false -ldflags="-s -w" \
      -o /out/gateway ./cmd/gateway

# --- runtime -------------------------------------------------------------
# Alpine rather than distroless: busybox wget backs the compose healthcheck,
# and a shell is worth a lot when a self-hoster is debugging their own box.
FROM alpine:3.22

# ca-certificates so outbound delivery to HTTPS destinations can verify chains.
RUN apk add --no-cache ca-certificates \
 && adduser -D -u 10001 gateway

COPY --from=build /out/gateway /usr/local/bin/gateway

USER gateway
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/gateway"]
