import { existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';

const require = createRequire(import.meta.url);

// resvg's fontdb accepts TTF/OTF only — the Fontsource woff/woff2 files
// do not rasterize — so the embedded faces come from the Expo Google
// Fonts package (TTF). Only the four weights Vega-Lite's theme
// requests are embedded; the list is resolved once per process.
const FILES = [
  '400Regular/IBMPlexSans_400Regular.ttf',
  '500Medium/IBMPlexSans_500Medium.ttf',
  '600SemiBold/IBMPlexSans_600SemiBold.ttf',
  '700Bold/IBMPlexSans_700Bold.ttf',
];

let cached;

export function fontFiles() {
  if (!cached) {
    const pkgDir = path.dirname(require.resolve('@expo-google-fonts/ibm-plex-sans/package.json'));
    cached = FILES.map((f) => path.join(pkgDir, f));
    for (const f of cached) {
      if (!existsSync(f)) throw new Error(`missing font file ${f}`);
    }
  }
  return cached;
}
