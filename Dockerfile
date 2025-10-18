FROM golang:1.24.0-alpine3.21 AS builder

WORKDIR /app

COPY . /app

RUN apk add --no-cache gcc musl-dev git

RUN go mod download

RUN go get -u go.mau.fi/whatsmeow

RUN go mod tidy

RUN CGO_ENABLED=1 go build -o whatsapp-worker

FROM alpine:latest

WORKDIR /root/

RUN mkdir storage

COPY --from=builder /app/whatsapp-worker .

CMD ["./whatsapp-worker"]
