import zlib from 'node:zlib';

// Minimal PNG decoder for tests: parses IHDR and inflates/unfilters
// IDAT so tests can count real pixels. resvg emits 8-bit RGBA
// (color type 6), non-interlaced.
export function decodePng(buf) {
  if (!buf.subarray(0, 8).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))) {
    throw new Error('not a PNG');
  }
  let off = 8;
  let w;
  let h;
  let bitDepth;
  let colorType;
  const idat = [];
  while (off < buf.length) {
    const len = buf.readUInt32BE(off);
    const type = buf.toString('ascii', off + 4, off + 8);
    const data = buf.subarray(off + 8, off + 8 + len);
    if (type === 'IHDR') {
      w = data.readUInt32BE(0);
      h = data.readUInt32BE(4);
      bitDepth = data[8];
      colorType = data[9];
    } else if (type === 'IDAT') {
      idat.push(data);
    } else if (type === 'IEND') {
      break;
    }
    off += 12 + len;
  }
  if (w === undefined) throw new Error('PNG has no IHDR');
  if (bitDepth !== 8 || colorType !== 6) throw new Error(`unsupported PNG format depth=${bitDepth} ct=${colorType}`);
  const channels = 4;
  const stride = w * channels + 1;
  const raw = zlib.inflateSync(Buffer.concat(idat));
  if (raw.length < h * stride) throw new Error('truncated IDAT');
  const out = Buffer.alloc(w * h * channels);
  const prev = Buffer.alloc(w * channels);
  for (let y = 0; y < h; y += 1) {
    const rowStart = y * stride;
    const filter = raw[rowStart];
    const row = raw.subarray(rowStart + 1, rowStart + stride);
    const cur = out.subarray(y * w * channels, (y + 1) * w * channels);
    for (let x = 0; x < w * channels; x += 1) {
      const a = x >= channels ? cur[x - channels] : 0;
      const b = prev[x];
      const c = x >= channels ? prev[x - channels] : 0;
      let v = row[x];
      if (filter === 1) v = (v + a) & 0xff;
      else if (filter === 2) v = (v + b) & 0xff;
      else if (filter === 3) v = (v + ((a + b) >> 1)) & 0xff;
      else if (filter === 4) {
        const p = a + b - c;
        const pa = Math.abs(p - a);
        const pb = Math.abs(p - b);
        const pc = Math.abs(p - c);
        v = (v + (pa <= pb && pa <= pc ? a : pb <= pc ? b : c)) & 0xff;
      }
      cur[x] = v;
    }
    prev.set(cur);
  }
  return { width: w, height: h, channels, data: out };
}

// countNonWhite counts pixels that differ from opaque white — axes,
// marks, text and gridlines all count; the white underlay does not.
export function countNonWhite(png) {
  const p = decodePng(png);
  let n = 0;
  for (let i = 0; i < p.data.length; i += p.channels) {
    if (p.data[i] !== 255 || p.data[i + 1] !== 255 || p.data[i + 2] !== 255 || p.data[i + 3] !== 255) n += 1;
  }
  return { width: p.width, height: p.height, nonWhite: n };
}
