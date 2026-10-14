# ai-service（quant-trading-agent 仓）的镜像构建。
#
# 关键点：agent 的 go.mod 用 `replace ... => ../quant-trading` 指向 core 仓，
# Docker 构建里这个相对路径由**命名构建上下文 `core`** 提供（compose 的
# additional_contexts: core=../quant-trading）。core 源码必须落在
# /build/quant-trading，replace 才能解析。
#
# 本地 compose（docker-compose.yml in core 仓）已配置；独立构建示例：
#   docker build -t ai-service \
#     --build-context core=../quant-trading \
#     ../quant-trading-agent

# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /build/agent

# 依赖清单先行（层缓存友好）
COPY go.mod go.sum* ./

# core 仓源码（命名上下文）—— replace 目标
COPY --from=core . /build/quant-trading

# agent 源码
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/ai-service ./cmd/ai

# Final stage
FROM alpine:3.19

WORKDIR /app

RUN apk add --no-cache ca-certificates curl

COPY --from=builder /app/ai-service /app/ai-service

# 依赖等待脚本取自 core 仓（命名上下文 `core`），与 core 三个服务同一份。
# compose 的 entrypoint 引用 /app/wait-for-deps.sh；少了这一行容器会
# `can't open ...: No such file or directory` 直接退出（拆仓时漏带）。
COPY --from=core deploy/wait-for-deps.sh /app/wait-for-deps.sh
RUN chmod +x /app/wait-for-deps.sh

# 配置也取自 core 仓：cmd/ai/main.go 读的是相对路径 config/ai-service.yaml，
# 而该文件与 analysis-service.yaml **逐值一致**（同一实验不得在两个进程跑出
# 不同结果），所以它的真相留在 core 的 config/ 目录，不在这里复制第二份。
COPY --from=core config/ai-service.yaml /app/config/ai-service.yaml

EXPOSE 8086

CMD ["/app/ai-service"]
