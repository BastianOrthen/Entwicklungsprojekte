# Multi-target Dockerfile to build server, simulator and web artifacts.
# Use build-time arg TARGET to produce different images: server | simulator | web

FROM --platform=$BUILDPLATFORM golang:1.20-alpine AS builder
RUN apk add --no-cache git build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGET=server
ARG TARGETOS=linux
ARG TARGETARCH
RUN if [ "$TARGET" = "server" ]; then \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=${TARGETARCH:-amd64} go build -o /out/adsb-server ./cmd/server; fi
RUN if [ "$TARGET" = "simulator" ]; then \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=${TARGETARCH:-amd64} go build -o /out/simulator ./cmd/simulator; fi
RUN if [ "$TARGET" = "internet" ]; then \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=${TARGETARCH:-amd64} go build -o /out/internet ./cmd/internet; fi
RUN if [ "$TARGET" = "web" ]; then \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=${TARGETARCH:-amd64} go build -o /out/web ./cmd/web; \
    mkdir -p /out/assets && cp -r web/* /out/assets/; fi

FROM alpine:3.18 AS runtime
RUN apk add --no-cache ca-certificates
WORKDIR /app
ARG TARGET=server
ENV TARGET=$TARGET
COPY --from=builder /out /out
RUN if [ "$TARGET" = "server" ]; then mv /out/adsb-server /app/adsb-server; fi
RUN if [ "$TARGET" = "simulator" ]; then mv /out/simulator /app/simulator; fi
RUN if [ "$TARGET" = "internet" ]; then mv /out/internet /app/internet; fi
RUN if [ "$TARGET" = "web" ]; then mv /out/web /app/web && mv /out/assets /app/assets; fi

ENV PORT=8080
EXPOSE 8080 50051 3000
CMD ["/bin/sh","-c","if [ \"$TARGET\" = \"server\" ]; then /app/adsb-server; elif [ \"$TARGET\" = \"simulator\" ]; then /app/simulator; elif [ \"$TARGET\" = \"internet\" ]; then /app/internet; else /app/web -http :3000 -root /app/assets; fi"]
