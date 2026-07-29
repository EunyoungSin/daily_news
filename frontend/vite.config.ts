import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
    // WSL 환경에서는 /mnt/c(DrvFs) 아래의 프로젝트가 inotify 이벤트를
    // chokidar에 안정적으로 전달하지 못해서, HMR이 파일 수정 감지를 조용히
    // 멈춰버린다. polling으로 이를 우회한다 — 네이티브 Linux/Mac에서는
    // 비용이 들지 않는 no-op이다.
    watch: {
      usePolling: true,
    },
  },
  build: {
    // go:embed는 backend 모듈 안에 있는 파일만 참조할 수 있으므로, 프로덕션
    // 빌드 결과물을 곧바로 backend/static에 내보낸다.
    outDir: '../backend/static',
    emptyOutDir: true,
  },
})
