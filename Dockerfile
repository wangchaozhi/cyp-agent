FROM node:20.19-alpine AS web-build
WORKDIR /src/apps/web
COPY apps/web/package.json apps/web/package-lock.json ./
RUN npm ci
COPY apps/web/ ./
RUN npm run build

FROM golang:1.25-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN version="$(tr -d '\r\n' < VERSION)" && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${version}" -o /out/cyp-server ./cmd/cyp-server && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${version}" -o /out/cyp ./cmd/cyp

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S cyp && adduser -S -G cyp -h /app cyp
WORKDIR /app
COPY --from=go-build /out/cyp-server /usr/local/bin/cyp-server
COPY --from=go-build /out/cyp /usr/local/bin/cyp
COPY --from=web-build /src/apps/web/dist /app/web
RUN mkdir -p /app/data && chown -R cyp:cyp /app
USER cyp
EXPOSE 8000
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8000/api/health >/dev/null || exit 1
ENTRYPOINT ["cyp-server"]
CMD ["-host", "0.0.0.0", "-port", "8000", "-web-dir", "/app/web"]
