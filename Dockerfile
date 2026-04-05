FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bridge-acp ./cmd/bridge-acp

FROM ubuntu:22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    gnupg \
    && mkdir -p /etc/apt/keyrings \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

RUN npm install -g opencode-ai

RUN useradd -r -m -s /bin/bash app

WORKDIR /opt/acpbridge

COPY --from=builder /app/bridge-acp .

RUN chown -R app:app /opt/acpbridge

USER app

EXPOSE 9091

CMD ["./bridge-acp", "--config", "config.json"]
