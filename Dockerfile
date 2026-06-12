# Build stage
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /corridord ./cmd/corridord

# Run stage.
# WHY alpine over scratch/distroless: we need CA certificates for venue
# TLS anyway, and a shell makes production debugging of the one binary
# that must never stay down much faster.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H corridor
COPY --from=build /corridord /usr/local/bin/corridord
USER corridor
EXPOSE 8080
ENTRYPOINT ["corridord"]
