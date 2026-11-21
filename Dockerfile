# containerservice: a static Go binary on distroless. (The web UI is embedded once it exists.)
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/containerservice ./cmd/containerservice

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/containerservice /usr/local/bin/containerservice
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/containerservice"]
