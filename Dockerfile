# syntax=docker/dockerfile:1
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/aegis ./cmd/aegis

# Distroless: no shell, no package manager, runs as a non-root user. About 10 MB in total.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aegis /aegis
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/aegis"]
CMD ["serve", "-config", "/etc/aegis/aegis.json"]
