# containerservice: the Next.js export of web/, built from its lockfile and embedded into a static Go
# binary on distroless, the bastion (/usr/local/bin/bastion) and the FUSE device plugin. Whatever a local build left in
# internal/web/ui is discarded first.
FROM node:24 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN find internal/web/ui -mindepth 1 ! -name .gitignore -delete
COPY --from=web /web/out/ internal/web/ui/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/ ./cmd/containerservice ./cmd/bastion ./cmd/fuse-device-plugin

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/containerservice /out/bastion /out/fuse-device-plugin /usr/local/bin/
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/containerservice"]
