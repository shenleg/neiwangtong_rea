# 本文件静态构建独立 Web 客户端，最小 Linux 镜像默认以 root 运行并将资料存入 /data。
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/nwtweb/ ./cmd/nwtweb/
COPY internal/nativeweb/ ./internal/nativeweb/
COPY internal/nativeproto/ ./internal/nativeproto/
RUN CGO_ENABLED=0 GOOS=linux go build -mod=readonly -trimpath -o /out/nwtweb ./cmd/nwtweb \
    && mkdir -p /out/data \
    && chmod 0700 /out/data

FROM scratch
COPY --from=build /out/nwtweb /nwtweb
# COPY 新建目标目录时不会沿用源目录权限，显式保留0700；不修改挂载目录的归属。
COPY --from=build --chmod=0700 /out/data /data
STOPSIGNAL SIGTERM
ENTRYPOINT ["/nwtweb"]
CMD ["-data-dir","/data"]
