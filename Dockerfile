# syntax=docker/dockerfile:1

# One build stage compiles every binary; each runtime target copies one.
# The hub is the last stage, so a plain `docker build .` produces it.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ \
    ./cmd/hub ./dev/webhooksink ./dev/mockmidtrans ./dev/demo

# Local testing tools (docker-compose.yml)
FROM gcr.io/distroless/static-debian12:nonroot AS webhook-sink
COPY --from=build /out/webhooksink /webhook-sink
EXPOSE 8090
ENTRYPOINT ["/webhook-sink"]

FROM gcr.io/distroless/static-debian12:nonroot AS mock-midtrans
COPY --from=build /out/mockmidtrans /mock-midtrans
EXPOSE 8081
ENTRYPOINT ["/mock-midtrans"]

FROM gcr.io/distroless/static-debian12:nonroot AS demo
COPY --from=build /out/demo /demo
ENTRYPOINT ["/demo"]

# Production image
FROM gcr.io/distroless/static-debian12:nonroot AS hub
COPY --from=build /out/hub /hub
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD ["/hub", "-healthcheck"]
ENTRYPOINT ["/hub"]
