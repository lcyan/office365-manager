# 构建阶段：静态编译单二进制。
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd cmd
COPY internal internal
COPY assets assets
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/office365-manager ./cmd/server

# 运行阶段：distroless 静态镜像（无 shell，体积约 20MB）。
FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/office365-manager /app/office365-manager
ENV CONFIG_FILE=/app/config.json
EXPOSE 5000
ENTRYPOINT ["/app/office365-manager"]
