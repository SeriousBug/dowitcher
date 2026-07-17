// Regenerates the app icons in web/public. Run with `node scripts/make-icons.mjs`.
//
// The icons are drawn here rather than kept as opaque binaries so that a colour
// change in panda.config.ts can be followed through by hand, and so no image
// tooling has to be installed to rebuild them. Node's zlib is the only thing a
// PNG actually needs.
import { deflateSync } from "node:zlib";
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath, URL } from "node:url";

const INK_900 = [0x13, 0x10, 0x11];
const INK_600 = [0x4b, 0x41, 0x45];
const INK_500 = [0x6d, 0x61, 0x65];
const MAGENTA_500 = [0xe9, 0x1e, 0x84];

// 4x supersampling: the corner radius and the bar edges are the only curves in
// the mark, and averaging down is cheaper to write than an AA rasteriser.
const SS = 4;

/** The wordmark seen end-on: spines standing in a box, one pulled proud. */
const BARS = [
  { color: INK_500, height: 0.62 },
  { color: MAGENTA_500, height: 1.0 },
  { color: INK_500, height: 0.76 },
  { color: INK_600, height: 0.5 },
];

function inRoundedRect(x, y, rect) {
  const { x0, y0, x1, y1, r } = rect;
  if (x < x0 || y < y0 || x > x1 || y > y1) return false;
  const cx = Math.min(Math.max(x, x0 + r), x1 - r);
  const cy = Math.min(Math.max(y, y0 + r), y1 - r);
  return (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
}

/**
 * @param size    edge length in pixels
 * @param maskable full-bleed background with the glyph inside the 80% safe zone,
 *                 per the maskable icon spec — the launcher crops the rest.
 */
function drawIcon(size, maskable) {
  const s = size * SS;
  const rgba = new Uint8Array(s * s * 4);

  const inset = maskable ? 0 : s * 0.06;
  const plate = {
    x0: inset,
    y0: inset,
    x1: s - inset,
    y1: s - inset,
    r: maskable ? 0 : (s - 2 * inset) * 0.22,
  };

  // Safe zone is the central 80% for maskable; an "any" icon can fill its plate.
  const glyph = (maskable ? 0.56 : 0.66) * s;
  const gx0 = (s - glyph) / 2;
  const baseline = (s + glyph) / 2;
  const barW = glyph * 0.16;
  const gap = (glyph - BARS.length * barW) / (BARS.length - 1);

  const shapes = BARS.map((bar, i) => ({
    color: bar.color,
    rect: {
      x0: gx0 + i * (barW + gap),
      y0: baseline - glyph * bar.height,
      x1: gx0 + i * (barW + gap) + barW,
      y1: baseline,
      r: barW * 0.3,
    },
  }));

  for (let y = 0; y < s; y++) {
    for (let x = 0; x < s; x++) {
      let color = null;
      if (inRoundedRect(x + 0.5, y + 0.5, plate)) color = INK_900;
      for (const shape of shapes) {
        if (inRoundedRect(x + 0.5, y + 0.5, shape.rect)) color = shape.color;
      }
      const o = (y * s + x) * 4;
      if (color) {
        rgba[o] = color[0];
        rgba[o + 1] = color[1];
        rgba[o + 2] = color[2];
        rgba[o + 3] = 0xff;
      }
    }
  }

  return downsample(rgba, s, size);
}

function downsample(src, srcSize, size) {
  const out = new Uint8Array(size * size * 4);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let r = 0;
      let g = 0;
      let b = 0;
      let a = 0;
      for (let dy = 0; dy < SS; dy++) {
        for (let dx = 0; dx < SS; dx++) {
          const o = ((y * SS + dy) * srcSize + (x * SS + dx)) * 4;
          // Premultiply so transparent pixels don't drag colour into the edges.
          const alpha = src[o + 3] / 255;
          r += src[o] * alpha;
          g += src[o + 1] * alpha;
          b += src[o + 2] * alpha;
          a += src[o + 3];
        }
      }
      const n = SS * SS;
      const alpha = a / n / 255;
      const o = (y * size + x) * 4;
      out[o] = alpha ? Math.round(r / n / alpha) : 0;
      out[o + 1] = alpha ? Math.round(g / n / alpha) : 0;
      out[o + 2] = alpha ? Math.round(b / n / alpha) : 0;
      out[o + 3] = Math.round(a / n);
    }
  }
  return out;
}

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(buf) {
  let c = 0xffffffff;
  for (const byte of buf) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const head = Buffer.alloc(8);
  head.writeUInt32BE(data.length, 0);
  head.write(type, 4, "ascii");
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])), 0);
  return Buffer.concat([head, data, crc]);
}

function encodePNG(rgba, size) {
  // Filter type 0 (none) on every scanline: these are flat-colour icons, so the
  // filters that help photographs would only cost time.
  const raw = Buffer.alloc(size * (size * 4 + 1));
  for (let y = 0; y < size; y++) {
    raw[y * (size * 4 + 1)] = 0;
    Buffer.from(rgba.buffer, y * size * 4, size * 4).copy(raw, y * (size * 4 + 1) + 1);
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // colour type: RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

const outDir = fileURLToPath(new URL("../public", import.meta.url));
mkdirSync(outDir, { recursive: true });

for (const [name, size, maskable] of [
  ["icon-192.png", 192, false],
  ["icon-512.png", 512, false],
  ["icon-maskable-192.png", 192, true],
  ["icon-maskable-512.png", 512, true],
]) {
  writeFileSync(`${outDir}/${name}`, encodePNG(drawIcon(size, maskable), size));
  console.log(`wrote public/${name}`);
}
