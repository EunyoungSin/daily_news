# 프론트엔드(Vite) 빌드 → backend/static 산출 → Go 바이너리에 go:embed로 포함
# → 최종적으로 백엔드 하나만 떠 있으면 API + 정적 파일을 모두 서빙한다.
# Render 등 무료 호스팅에서 "Docker" 런타임으로 배포할 때 이 Dockerfile을 그대로 사용하면 된다.

FROM node:20-alpine AS frontend-build
WORKDIR /repo
COPY frontend/package.json frontend/package-lock.json ./frontend/
WORKDIR /repo/frontend
RUN npm ci
WORKDIR /repo
COPY frontend ./frontend
COPY backend ./backend
WORKDIR /repo/frontend
RUN npm run build

FROM golang:1.24-alpine AS backend-build
WORKDIR /repo/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend ./
COPY --from=frontend-build /repo/backend/static ./static
RUN CGO_ENABLED=0 go build -o dashboard-server .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
ENV TZ=Asia/Seoul
WORKDIR /app
COPY --from=backend-build /repo/backend/dashboard-server ./dashboard-server
EXPOSE 8080
CMD ["./dashboard-server"]
