FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lb ./cmd/lb && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backend ./cmd/backend
FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
COPY --from=build /out/ /usr/local/bin/
WORKDIR /app
COPY configs/ configs/
USER app
EXPOSE 8080 9090
ENTRYPOINT ["lb"]
CMD ["-config", "configs/docker.json"]
