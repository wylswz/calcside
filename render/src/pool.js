import { Worker } from 'node:worker_threads';
import { fileURLToPath } from 'node:url';

const WORKER_PATH = fileURLToPath(new URL('./worker.js', import.meta.url));

export class QueueFullError extends Error {
  constructor() {
    super('render queue full');
    this.name = 'QueueFullError';
  }
}

export class TimeoutError extends Error {
  constructor() {
    super('render timed out');
    this.name = 'TimeoutError';
  }
}

export class ClosedError extends Error {
  constructor() {
    super('render pool closed');
    this.name = 'ClosedError';
  }
}

// RenderJobError wraps a failure reported by the worker itself. code is
// 'spec' (denied/malformed spec → 400) or 'render' (compile/render → 422).
export class RenderJobError extends Error {
  constructor(code) {
    super(`render failed: ${code}`);
    this.name = 'RenderJobError';
    this.code = code;
  }
}

// WorkerDiedError marks a render lost to a crashed/killed worker — the
// pool respawns and the caller sees a transient 503.
export class WorkerDiedError extends Error {
  constructor() {
    super('render worker died');
    this.name = 'WorkerDiedError';
  }
}

// RenderPool is a fixed set of worker_threads rendering specs. At most
// `concurrency` renders run at once; further requests wait in a bounded
// queue and overflow with QueueFullError. Each render has a hard
// timeout — on expiry the worker is terminated and replaced.
export class RenderPool {
  constructor({ concurrency = 2, queueSize = 16, timeoutMs = 5000 } = {}) {
    this.concurrency = concurrency;
    this.queueSize = queueSize;
    this.timeoutMs = timeoutMs;
    this.slots = [];
    this.queue = [];
    this.nextId = 0;
    this.closed = false;
    for (let i = 0; i < concurrency; i += 1) this._spawn();
  }

  // render(specReq) → Promise<{png: Buffer, width, height}>.
  // specReq: {spec, scale?, maxSide?, delayMs?} — delayMs is a test hook.
  render(specReq) {
    if (this.closed) return Promise.reject(new ClosedError());
    return new Promise((resolve, reject) => {
      const job = { req: specReq, resolve, reject, timer: null };
      const idle = this.slots.find((s) => !s.busy);
      if (idle) {
        this._dispatch(idle, job);
      } else if (this.queue.length >= this.queueSize) {
        reject(new QueueFullError());
      } else {
        this.queue.push(job);
      }
    });
  }

  async close() {
    this.closed = true;
    for (const job of this.queue.splice(0)) job.reject(new ClosedError());
    for (const slot of this.slots.splice(0)) {
      if (slot.job) {
        clearTimeout(slot.job.timer);
        slot.job.reject(new ClosedError());
        slot.job = null;
      }
      slot.busy = false;
      await slot.worker.terminate();
    }
  }

  _spawn() {
    const slot = { worker: new Worker(WORKER_PATH), busy: false, job: null };
    slot.worker.on('message', (msg) => this._onMessage(slot, msg));
    slot.worker.on('error', () => this._onWorkerLost(slot));
    slot.worker.on('exit', (code) => {
      if (code !== 0 && this.slots.includes(slot)) this._onWorkerLost(slot);
    });
    this.slots.push(slot);
    this._pump();
  }

  _onMessage(slot, msg) {
    const job = slot.job;
    if (!job) return;
    slot.busy = false;
    slot.job = null;
    clearTimeout(job.timer);
    if (msg.ok) {
      job.resolve({ png: Buffer.from(msg.png), width: msg.width, height: msg.height });
    } else {
      job.reject(new RenderJobError(msg.code || 'render'));
    }
    this._pump();
  }

  _onWorkerLost(slot) {
    const idx = this.slots.indexOf(slot);
    if (idx < 0) return;
    this.slots.splice(idx, 1);
    if (slot.job) {
      clearTimeout(slot.job.timer);
      slot.job.reject(new WorkerDiedError());
      slot.job = null;
    }
    slot.busy = false;
    slot.worker.terminate().catch(() => {});
    if (!this.closed) this._spawn();
    this._pump();
  }

  _onTimeout(slot) {
    const job = slot.job;
    if (!job) return;
    slot.job = null;
    slot.busy = false;
    job.reject(new TimeoutError());
    // A wedged worker cannot be trusted — kill it and respawn.
    const idx = this.slots.indexOf(slot);
    if (idx >= 0) this.slots.splice(idx, 1);
    slot.worker.terminate().catch(() => {});
    if (!this.closed) this._spawn();
    this._pump();
  }

  _dispatch(slot, job) {
    slot.busy = true;
    slot.job = job;
    this.nextId += 1;
    job.timer = setTimeout(() => this._onTimeout(slot), this.timeoutMs);
    job.timer.unref?.();
    slot.worker.postMessage(
      { id: this.nextId, spec: job.req.spec, scale: job.req.scale, maxSide: job.req.maxSide, delayMs: job.req.delayMs },
      [],
    );
  }

  _pump() {
    while (this.queue.length > 0) {
      const idle = this.slots.find((s) => !s.busy);
      if (!idle) return;
      this._dispatch(idle, this.queue.shift());
    }
  }
}
