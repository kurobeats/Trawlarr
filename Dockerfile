# syntax=docker/dockerfile:1

FROM node:22-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY ui/ ./
COPY internal/web/dist/index.html ../internal/web/dist/index.html
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/web/dist ./internal/web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/trawlarr ./cmd/trawlarr

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
# No hostname spoofing anywhere: outgoing mail advertises the From domain, and
# the container hostname is never sent to a relay or written into a Message-ID.
COPY --from=build /out/trawlarr /app/trawlarr
VOLUME ["/data"]
ENV TRAWLARR_CONFIG=/data/config.json
USER nonroot:nonroot
EXPOSE 3333 8080
ENTRYPOINT ["/app/trawlarr"]
CMD ["-config", "/data/config.json"]
