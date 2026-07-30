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
    rolldownOptions: {
      output: {
        // 파일만 나눠서는(React.lazy) 청크가 자동으로 갈라지지 않는
        // 라이브러리들을 명시적으로 분리한다:
        // - recharts는 환율 차트에서만 쓰이고 d3-*/victory-vendor 같은
        //   무거운 의존성을 딸려온다 → 'recharts' 청크.
        // - react/react-dom은 거의 매 배포마다 바뀌는 앱 코드와 달리
        //   훨씬 드물게 바뀌므로 따로 캐시되게 → 'vendor-react' 청크.
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined

          if (/[/\\]node_modules[/\\](react|react-dom|scheduler)[/\\]/.test(id)) {
            return 'vendor-react'
          }

          if (
            /[/\\]node_modules[/\\](recharts|d3-[^/\\]+|victory-vendor|@reduxjs|react-redux|redux|reselect|immer|es-toolkit|decimal\.js-light|eventemitter3|tiny-invariant|use-sync-external-store)[/\\]/.test(
              id,
            )
          ) {
            return 'recharts'
          }

          return undefined
        },
      },
    },
  },
})
