FROM public.ecr.aws/docker/library/golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./cmd/server

FROM public.ecr.aws/docker/library/alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/server .
COPY migrations/ ./migrations/
EXPOSE 8080
ENV MIGRATIONS_DIR=/app/migrations
CMD ["/app/server"]
