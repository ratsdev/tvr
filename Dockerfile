# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tvr ./cmd/tvr

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata ffmpeg \
  && adduser -D -H -u 1000 tvr \
  && mkdir -p /data \
  && chown tvr:tvr /data
COPY --from=build /out/tvr /usr/local/bin/tvr
USER tvr
WORKDIR /data
EXPOSE 8080
ENV TVR_LISTEN=:8080 \
    TVR_DATA_DIR=/data \
    TVR_DATABASE=/data/tvr.db
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/tvr"]
