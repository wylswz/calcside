import { parentPort } from 'node:worker_threads';

import { renderPng } from './render.js';

// One message per job; the reply carries either the PNG as a
// transferable ArrayBuffer or a short error code. Spec contents never
// leave the worker in error messages.
parentPort.on('message', (job) => {
  (async () => {
    try {
      // delayMs exists only so tests can hold a worker past the pool
      // timeout; production requests never set it.
      if (job.delayMs) await new Promise((r) => setTimeout(r, job.delayMs));
      const out = await renderPng(job.spec, { scale: job.scale, maxSide: job.maxSide });
      const ab = out.png.buffer.slice(out.png.byteOffset, out.png.byteOffset + out.png.byteLength);
      parentPort.postMessage({ id: job.id, ok: true, png: ab, width: out.width, height: out.height }, [ab]);
    } catch (e) {
      parentPort.postMessage({ id: job.id, ok: false, code: e && e.code ? e.code : 'render' });
    }
  })();
});
