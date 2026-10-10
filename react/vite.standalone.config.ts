import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
export default defineConfig({ plugins: [react()], build: { emptyOutDir: true, outDir: 'standalone', lib: { entry: 'src/standalone.tsx', formats: ['es'], fileName: () => 'browser.js' } } })
