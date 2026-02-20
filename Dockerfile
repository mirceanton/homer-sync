# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o homer-sync ./cmd

# Final stage – distroless provides CA certificates and nothing else,
# yielding the smallest possible attack surface.
FROM gcr.io/distroless/static-nonroot:nonroot

COPY --from=builder /app/homer-sync /homer-sync

USER nonroot:nonroot

ENTRYPOINT ["/homer-sync"]
