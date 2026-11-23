# The bench's traffic image (spec 025 T012 and T013): a plain
# OpenTelemetry Go SDK app. Build: docker build -t t012-otel:2 .
FROM golang:1.26-alpine AS b
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o /app .
FROM alpine:3.22
COPY --from=b /app /app
USER 10001
EXPOSE 8080
ENTRYPOINT ["/app"]
