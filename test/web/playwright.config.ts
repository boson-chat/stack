import { defineConfig } from '@playwright/test';

// The web client at https://WEB_HOST of a local stack (scripts/setup.sh
// --local: a local CA). The page is trusted with ignoreHTTPSErrors; QUIC
// (WebTransport to quicvoiced) by the local certificate's SPKI hash ($SPKI).
export default defineConfig({
  testDir: '.',
  timeout: 90_000,
  expect: { timeout: 20_000 },
  workers: 1,
  reporter: [['list']],
  use: {
    browserName: 'chromium',
    ignoreHTTPSErrors: true,
    permissions: ['microphone'],
    launchOptions: {
      args: [
        '--use-fake-device-for-media-stream',
        '--use-fake-ui-for-media-stream',
        // Chromium takes a QUIC certificate it can't verify only with both:
        // the key it may accept, and QUIC forced for those origins.
        ...(process.env['SPKI'] ? [
          `--ignore-certificate-errors-spki-list=${process.env['SPKI']}`,
          `--origin-to-force-quic-on=${process.env['VOICE_HOST'] ?? 'voice.localhost'}:7700,${process.env['VIDEO_HOST'] ?? 'video.localhost'}:7710`,
        ] : []),
      ],
    },
  },
});
