# Build một file chạy tĩnh, image cuối chỉ có file đó (distroless, không có shell).
# Stage build dùng bản alpine (~300 MB thay vì ~1 GB); image cuối khoảng 15 MB.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tingctl ./cmd/tingctl

# distroless/static có sẵn chứng chỉ CA (cần cho HTTPS tới Google và TLS tới Neon).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /out/tingctl /
USER nonroot
ENTRYPOINT ["/server"]
