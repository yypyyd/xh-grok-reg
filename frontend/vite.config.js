import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: { outDir: '../static', emptyOutDir: true },
  server: { port: 5173, proxy: { '/api': 'http://localhost:9000', '/healthz': 'http://localhost:9000' } }
})
