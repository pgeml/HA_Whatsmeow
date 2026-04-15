# syntax=docker/dockerfile:1
ARG BUILD_FROM=ghcr.io/home-assistant/aarch64-base:3.23
ARG GO_VERSION=1.25.5

FROM golang:${GO_VERSION}-alpine AS builder
ENV GOTOOLCHAIN=local
RUN apk add --no-cache git ca-certificates build-base
WORKDIR /src
COPY app/ /src/
RUN go mod tidy
RUN go mod download
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/whatsapp-addon ./cmd/addon

FROM ${BUILD_FROM}

RUN apk add --no-cache ca-certificates tzdata bash

COPY --from=builder /out/whatsapp-addon /usr/local/bin/whatsapp-addon
COPY custom_component/ /custom_component/
COPY rootfs/ /

EXPOSE 3000
