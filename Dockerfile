FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26.5-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/to-alan/vaultmesh/internal/version.Version=${VERSION} -X github.com/to-alan/vaultmesh/internal/version.Commit=${COMMIT} -X github.com/to-alan/vaultmesh/internal/version.Date=${DATE}" \
    -o /out/vaultmesh-server ./cmd/vaultmesh-server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=server /out/vaultmesh-server /usr/local/bin/vaultmesh-server
COPY --from=web /src/web/dist /app/web
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/vaultmesh-server"]
