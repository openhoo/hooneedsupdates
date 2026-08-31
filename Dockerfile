ARG VERSION=dev

FROM golang:1.27.0-alpine AS build

ARG VERSION
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hooneedsupdates ./cmd/hooneedsupdates

FROM alpine:3.24.1

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
