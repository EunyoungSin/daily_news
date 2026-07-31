# 프론트엔드(Vite) 빌드 → backend/static 산출 → Go 바이너리에 go:embed로 포함
# → 최종적으로 백엔드 하나만 떠 있으면 API + 정적 파일을 모두 서빙한다.
# Render 등 무료 호스팅에서 "Docker" 런타임으로 배포할 때 이 Dockerfile을 그대로 사용하면 된다.
#
# DB 드라이버(github.com/tursodatabase/go-libsql)는 CGO로 네이티브 libSQL
# 라이브러리를 호출하므로 CGO_ENABLED=0으로는 아예 빌드되지 않는다(빌드
# 태그로 모든 소스 파일이 제외됨) — 그래서 빌드 스테이지는 CGO_ENABLED=1과
# C 컴파일러(gcc)가 필요하다. 이 드라이버가 배포하는 사전 컴파일된 정적
# 라이브러리(.a)는 musl(Alpine)이 아니라 glibc 기준으로 빌드되어 있어,
# 빌드/런타임 스테이지 모두 alpine이 아니라 Debian 계열(golang:1.24-bookworm
# / debian:bookworm-slim)을 쓴다 — Alpine을 쓰면 링크 실패나 런타임 "symbol
# not found"로 이어질 수 있다.

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

FROM golang:1.24-bookworm AS backend-build
RUN apt-get update && apt-get install -y --no-install-recommends gcc && rm -rf /var/lib/apt/lists/*
WORKDIR /repo/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend ./
COPY --from=frontend-build /repo/backend/static ./static
RUN CGO_ENABLED=1 go build -o dashboard-server .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
ENV TZ=Asia/Seoul
WORKDIR /app
COPY --from=backend-build /repo/backend/dashboard-server ./dashboard-server
EXPOSE 8080
CMD ["./dashboard-server"]
