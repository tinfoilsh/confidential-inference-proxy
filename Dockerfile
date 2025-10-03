FROM golang:1.25-alpine AS builder

WORKDIR /app

ARG VERSION=dev

COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X main.Version=${VERSION}" \
    -o proxy

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/proxy .

EXPOSE 8089

CMD ["./proxy"]
