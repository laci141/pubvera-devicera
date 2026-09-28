import { defineConfig, devices } from '@playwright/test';
import { execFileSync } from 'child_process';
import os from 'os';
import path from 'path';

// A free local port, chosen once. Workers load this file again, but they
// inherit the environment of the runner, so they see the same port.
if (!process.env.DEVICERA_E2E_PORT) {
  process.env.DEVICERA_E2E_PORT = execFileSync(process.execPath, [
    '-e',
    "const s=require('net').createServer();s.listen(0,'127.0.0.1',()=>{process.stdout.write(String(s.address().port));s.close();});",
  ]).toString().trim();
}
const port = process.env.DEVICERA_E2E_PORT;

// The real binary, built from this checkout. Outside the repository so the
// build output never shows up in git status.
const bin = path.join(os.tmpdir(), 'devicera-e2e', process.platform === 'win32' ? 'mdi.exe' : 'mdi');

export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `go build -o "${bin}" ./cmd/medical-device-intelligence-pp-cli && "${bin}" serve`,
    cwd: path.join(__dirname, '..'),
    url: `http://127.0.0.1:${port}/api/health`,
    // go build on a cold cache is the slow part.
    timeout: 180_000,
    reuseExistingServer: false,
    // Empty Supabase config: the page runs unauthenticated and never loads
    // the Supabase library. Auth lives in Caddy, not in this binary.
    env: { PORT: port, SUPABASE_URL: '', SUPABASE_PUBLISHABLE_KEY: '' },
    stdout: 'ignore',
    stderr: 'pipe',
  },
});
