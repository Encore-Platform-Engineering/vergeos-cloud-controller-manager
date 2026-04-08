# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /vergeos-ccm ./cmd/vergeos-cloud-controller-manager/

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /vergeos-ccm /usr/local/bin/vergeos-cloud-controller-manager

ENTRYPOINT ["/usr/local/bin/vergeos-cloud-controller-manager"]
