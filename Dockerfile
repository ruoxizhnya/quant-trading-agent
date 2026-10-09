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

EXPOSE 8086

CMD ["/app/ai-service"]
