FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /relay ./cmd/pocketsudo-relay

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /relay /relay
ENV LISTEN=:8080 DATA_DIR=/data TRUST_PROXY=1
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/relay", "healthcheck"]
ENTRYPOINT ["/relay"]
