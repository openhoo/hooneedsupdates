ARG VERSION=dev

FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build

ARG VERSION
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hooneedsupdates ./cmd/hooneedsupdates

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN apk add --no-cache ca-certificates
COPY --from=build /out/hooneedsupdates /usr/local/bin/hooneedsupdates
COPY LICENSE /usr/share/licenses/hooneedsupdates/LICENSE

LABEL org.opencontainers.image.title="HooNeedsUpdates" \
      org.opencontainers.image.description="Preview-first dependency update planning" \
      org.opencontainers.image.source="https://github.com/openhoo/hooneedsupdates" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

USER 65532:65532
ENTRYPOINT ["hooneedsupdates"]
