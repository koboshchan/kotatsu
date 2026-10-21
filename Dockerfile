FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/koboshchan/anyshell/internal/version.Version=$VERSION -X github.com/koboshchan/anyshell/internal/version.Commit=$COMMIT" -o /out/anyshell-server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=65532:65532 /out/anyshell-server /anyshell-server
COPY --from=build --chown=65532:65532 /tmp /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/anyshell-server"]
