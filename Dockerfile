# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Download dependencies first (cached layer unless go.mod / go.sum change)
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and build a statically linked binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /vaultnuban ./cmd/server

# ── Stage 2: run ──────────────────────────────────────────────────────────────
FROM alpine:3.21

# ca-certificates required for TLS calls to Nomba API and Neon (TLS PostgreSQL)
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /vaultnuban /app/vaultnuban

EXPOSE 8080

ENTRYPOINT ["/app/vaultnuban"]
