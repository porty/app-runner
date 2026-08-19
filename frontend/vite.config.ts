/// <reference types="vitest/config" />

import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/twirp': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      '/console': {
        target: 'ws://localhost:8080',
        ws: true,
      },
    },
  },
  test: {
    environment: 'node',
    restoreMocks: true,
  },
})
