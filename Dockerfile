FROM golang:1.23.4-alpine AS builder

WORKDIR /app

# 必要な依存関係をコピー
COPY go.mod go.sum ./
RUN go mod download

# アプリケーションコードをコピー
COPY . ./

# ビルド
RUN go build -o main .

# 実行環境
FROM alpine:latest

WORKDIR /root/

# 必要なファイルをコピー
COPY --from=builder /app/main .
COPY .env .env

# 実行
CMD ["./main"]