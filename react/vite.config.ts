import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
export default defineConfig({ plugins: [react()], build: { emptyOutDir: true, lib: { entry: 'src/index.ts', formats: ['es'], fileName: () => 'browser.js' }, rollupOptions: { external: ['react', 'react-dom', 'react/jsx-runtime', 'lucide-react'] } } });
