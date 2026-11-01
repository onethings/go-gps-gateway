# Build stage: a static binary, so the runtime image carries no libc.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/traccar-gateway ./cmd/traccar-gateway

FROM alpine:3.20
# ca-certificates for webhook HTTPS and SMTP STARTTLS, tzdata for the local
# timestamps the H02 and GT06 encoders build.
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/traccar-gateway /usr/local/bin/traccar-gateway

# H02, GT06, Teltonika, JT808 and the REST/metrics port.
EXPOSE 5013 5023 5027 8610 8082

ENTRYPOINT ["traccar-gateway"]
