# Builds oGit from source: the web UI, then a static binary, in a small
# Alpine image. Used by the release workflow (with VERSION set to the tag,
# on native amd64 and arm64 runners), docker-compose.yaml and `docker build .`.
# `--target binary --output type=local,dest=dist` exports just the binary.
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=source
# CGO for the SQLite driver; static, so the runtime image needs no libc.
RUN CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -linkmode external -extldflags -static -X main.version=${VERSION}" -o /out/app ./cmd/cmd.go

FROM scratch AS binary
COPY --from=build /out/app /ogit

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
WORKDIR /
COPY --from=build /out/app /app
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080 2222
ENTRYPOINT ["/entrypoint.sh"]
