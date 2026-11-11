// Serial CAS autosave queue — one drain promise, debounced,
// latest-wins, conflict is sticky until reset.
export interface AutosaveState<T> {
  dirty: boolean
  saving: boolean
  error?: Error
  conflict: boolean
  revision: number
  saved: { value: T; version: number }
  latest: T
}

type SaveResult<T> = { value: T; version: number }

export class AutosaveQueue<T> {
  private o: {
    fingerprint: (v: T) => string
    save: (v: T, ver: number) => Promise<SaveResult<T>>
    reconcile: (cur: T, sent: T, ack: T) => T
    onChange?: (s: AutosaveState<T>) => void
    delayMs: number
  }
  private state: AutosaveState<T>
  private timer?: ReturnType<typeof setTimeout>
  private flight?: Promise<SaveResult<T>>
  private epoch = 0
  private dead = false
  private initialDirty: boolean

  constructor(o: {
    initial: SaveResult<T>
    fingerprint: (v: T) => string
    save: (v: T, ver: number) => Promise<SaveResult<T>>
    reconcile?: (cur: T, sent: T, ack: T) => T
    onChange?: (s: AutosaveState<T>) => void
    delayMs?: number
    initialDirty?: boolean
  }) {
    this.o = {
      fingerprint: o.fingerprint,
      save: o.save,
      reconcile: o.reconcile ?? ((cur, sent, ack) =>
        o.fingerprint(cur) === o.fingerprint(sent) ? ack : cur),
      onChange: o.onChange,
      delayMs: o.delayMs ?? 500,
    }
    this.state = {
      dirty: o.initialDirty ?? false, saving: false, conflict: false,
      revision: 0, saved: o.initial, latest: o.initial.value,
    }
    this.initialDirty = o.initialDirty ?? false
    if (this.state.dirty) this.schedule()
  }

  get snapshot(): AutosaveState<T> {
    return { ...this.state }
  }

  private notify() {
    if (this.dead) return
    this.o.onChange?.(this.snapshot)
  }

  update(value: T) {
    if (this.dead) return
    const fp = this.o.fingerprint
    if (fp(value) === fp(this.state.latest)) return // identical — no churn
    const dirty = this.initialDirty || fp(value) !== fp(this.state.saved.value)
    this.state = { ...this.state, latest: value, dirty, revision: this.state.revision + 1 }
    // A NEW edit clears a transient error and may reschedule; never
    // clears a 409 conflict.
    if (!this.state.conflict) {
      this.state = { ...this.state, error: undefined }
    }
    this.notify()
    this.schedule()
  }

  private schedule() {
    if (this.dead || this.state.conflict || this.state.error || !this.state.dirty) return
    if (this.timer) clearTimeout(this.timer)
    this.timer = setTimeout(() => {
      this.timer = undefined
      void this.flush().catch(() => undefined) // state already carries the failure
    }, this.o.delayMs)
  }

  private drain = async (): Promise<SaveResult<T>> => {
    const myEpoch = this.epoch
    while (this.state.dirty && !this.dead && !this.state.conflict && myEpoch === this.epoch) {
      const sent = this.state.latest
      try {
        const ack = await this.o.save(sent, this.state.saved.version)
        if (this.dead || myEpoch !== this.epoch) return ack
        this.initialDirty = false
        const fp = this.o.fingerprint
        const nextLatest = this.o.reconcile(this.state.latest, sent, ack.value)
        const stillDirty = fp(nextLatest) !== fp(ack.value)
        this.state = {
          ...this.state,
          saved: ack,
          latest: nextLatest,
          dirty: stillDirty,
          revision: this.state.revision + 1,
        }
        this.notify()
        if (!stillDirty) return ack
      } catch (e) {
        if (this.dead || myEpoch !== this.epoch) throw e
        const st = (e as { status?: number }).status
        this.state = {
          ...this.state,
          error: e as Error,
          conflict: st === 409,
        }
        this.notify()
        throw e
      }
    }
    return this.state.saved
  }

  async flush(): Promise<SaveResult<T>> {
    if (this.timer) { clearTimeout(this.timer); this.timer = undefined }
    if (this.dead) return this.state.saved
    if (this.state.conflict) throw new Error('conflict')
    if (this.state.error) throw this.state.error
    if (this.flight) return this.flight
    if (!this.state.dirty) return this.state.saved
    this.state = { ...this.state, saving: true }
    this.notify()
    const p = this.drain().finally(() => {
      this.flight = undefined
      if (!this.dead) {
        this.state = { ...this.state, saving: false }
        this.notify()
        this.schedule()
      }
    })
    this.flight = p
    return p
  }

  async retry(): Promise<SaveResult<T>> {
    if (this.state.conflict) throw new Error('conflict')
    this.state = { ...this.state, error: undefined }
    this.notify()
    return this.flush()
  }

  // resolveConflict re-bases a 409-stuck queue on a fresh server read.
  // 'reload' adopts the server value and drops the draft; 'draft' keeps
  // the draft and re-bases its CAS version on the fresh revision, so the
  // next save explicitly overwrites what arrived meanwhile.
  resolveConflict(mode: 'reload' | 'draft', fresh: SaveResult<T>) {
    this.epoch += 1 // in-flight acks can no longer write to state
    const draft = this.state.latest
    this.state = {
      dirty: false, saving: false, conflict: false,
      revision: this.state.revision + 1, saved: fresh, latest: fresh.value,
    }
    if (mode === 'draft') {
      const fp = this.o.fingerprint
      const stillDirty = fp(draft) !== fp(fresh.value)
      this.state = {
        ...this.state,
        latest: draft,
        dirty: stillDirty,
        saved: { value: fresh.value, version: fresh.version },
      }
      if (stillDirty) this.schedule()
    }
    this.notify()
  }

  reset(v: SaveResult<T>) {
    this.epoch += 1 // in-flight acks can no longer write to state
    this.initialDirty = false
    this.state = {
      dirty: false, saving: false, conflict: false,
      revision: this.state.revision + 1, saved: v, latest: v.value,
    }
    this.notify()
  }

  dispose() {
    this.epoch += 1
    this.dead = true
    if (this.timer) { clearTimeout(this.timer); this.timer = undefined }
  }
}
