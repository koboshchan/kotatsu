FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/koboshchan/kotatsu/internal/version.Version=$VERSION -X github.com/koboshchan/kotatsu/internal/version.Commit=$COMMIT" -o /out/kotatsu-server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=65532:65532 /out/kotatsu-server /kotatsu-server
COPY --from=build --chown=65532:65532 /tmp /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/kotatsu-server"]
