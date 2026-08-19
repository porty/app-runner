/// <reference types="vitest/config" />

import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/twirp': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
      '/console': {
        target: 'ws://127.0.0.1:8080',
        ws: true,
      },
    },
  },
  test: {
    environment: 'node',
    restoreMocks: true,
  },
})
