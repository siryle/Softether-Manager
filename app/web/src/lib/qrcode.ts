/**
 * Zero-dependency ISO/IEC 18004 QR Code encoder (Byte mode, EC Level M, Versions 1-20).
 *
 * Designed for WireGuard .conf payloads (~180-400 UTF-8 bytes) and general text
 * up to 666 bytes, rendering cleanly as a single crisp SVG path with a 4-module
 * quiet zone.
 */

export interface QrMatrix {
  version: number;
  size: number;
  /** Row-major module grid (`1` = dark, `0` = light). */
  modules: Uint8Array;
}

// ISO/IEC 18004 Table 9 — EC Level M: [totalDataCodewords, ecCodewordsPerBlock, group1Blocks, group1Data, group2Blocks, group2Data]
const EC_M_TABLE: ReadonlyArray<readonly [number, number, number, number, number, number]> = [
  [0, 0, 0, 0, 0, 0], // placeholder 0
  [16, 10, 1, 16, 0, 0], // v1
  [28, 16, 1, 28, 0, 0], // v2
  [44, 26, 1, 44, 0, 0], // v3
  [64, 18, 2, 32, 0, 0], // v4
  [86, 24, 2, 43, 0, 0], // v5
  [108, 16, 4, 27, 0, 0], // v6
  [124, 18, 4, 31, 0, 0], // v7
  [154, 22, 2, 38, 2, 39], // v8
  [182, 22, 3, 36, 2, 37], // v9
  [216, 26, 4, 43, 1, 44], // v10
  [254, 30, 1, 50, 4, 51], // v11
  [290, 22, 6, 36, 2, 37], // v12
  [334, 22, 8, 37, 1, 38], // v13
  [365, 24, 4, 40, 5, 41], // v14
  [415, 24, 5, 41, 5, 42], // v15
  [453, 28, 7, 45, 3, 46], // v16
  [507, 28, 10, 46, 1, 47], // v17
  [563, 26, 9, 43, 4, 44], // v18
  [627, 26, 3, 44, 11, 45], // v19
  [669, 26, 3, 41, 13, 42], // v20
];

// Alignment pattern center coordinates for versions 1-20.
const ALIGNMENT_POSITIONS: ReadonlyArray<readonly number[]> = [
  [], // 0
  [], // 1
  [6, 18],
  [6, 22],
  [6, 26],
  [6, 30],
  [6, 34],
  [6, 22, 38],
  [6, 24, 42],
  [6, 26, 46],
  [6, 28, 50],
  [6, 30, 54],
  [6, 32, 58],
  [6, 34, 62],
  [6, 26, 46, 66],
  [6, 26, 48, 70],
  [6, 26, 50, 74],
  [6, 30, 54, 78],
  [6, 30, 56, 82],
  [6, 30, 58, 86],
  [6, 34, 62, 90],
];

// GF(256) with primitive polynomial 0x11d.
const GF_EXP = new Uint8Array(512);
const GF_LOG = new Uint8Array(256);
{
  let x = 1;
  for (let i = 0; i < 255; i++) {
    GF_EXP[i] = x;
    GF_LOG[x] = i;
    x <<= 1;
    if (x & 0x100) x ^= 0x11d;
  }
  for (let i = 255; i < 512; i++) GF_EXP[i] = GF_EXP[i - 255];
}

function gfMul(a: number, b: number): number {
  return a === 0 || b === 0 ? 0 : GF_EXP[GF_LOG[a] + GF_LOG[b]];
}

function rsGenerator(degree: number): Uint8Array {
  let poly = new Uint8Array([1]);
  for (let i = 0; i < degree; i++) {
    const next = new Uint8Array(poly.length + 1);
    const factor = GF_EXP[i];
    for (let j = 0; j < poly.length; j++) {
      next[j] ^= poly[j];
      next[j + 1] ^= gfMul(poly[j], factor);
    }
    poly = next;
  }
  return poly;
}

function rsRemainder(data: Uint8Array, ecLen: number): Uint8Array {
  const gen = rsGenerator(ecLen);
  const res = new Uint8Array(ecLen);
  for (let i = 0; i < data.length; i++) {
    const factor = data[i] ^ res[0];
    res.copyWithin(0, 1);
    res[ecLen - 1] = 0;
    if (factor !== 0) {
      for (let j = 0; j < ecLen; j++) {
        res[j] ^= gfMul(gen[j + 1], factor);
      }
    }
  }
  return res;
}

function encodeDataBytes(bytes: Uint8Array, version: number): Uint8Array {
  const [totalData] = EC_M_TABLE[version];
  const countBits = version <= 9 ? 8 : 16;
  const bits: number[] = [];
  const pushBits = (val: number, len: number) => {
    for (let i = len - 1; i >= 0; i--) bits.push((val >>> i) & 1);
  };

  // Mode indicator: Byte (0100)
  pushBits(0b0100, 4);
  pushBits(bytes.length, countBits);
  for (let i = 0; i < bytes.length; i++) pushBits(bytes[i], 8);

  const capacityBits = totalData * 8;
  const term = Math.min(4, capacityBits - bits.length);
  pushBits(0, term);
  while (bits.length % 8 !== 0) bits.push(0);

  const out = new Uint8Array(totalData);
  for (let i = 0; i < bits.length; i += 8) {
    let b = 0;
    for (let j = 0; j < 8; j++) b = (b << 1) | bits[i + j];
    out[i >> 3] = b;
  }
  let idx = bits.length >> 3;
  const pads = [0xec, 0x11];
  let p = 0;
  while (idx < totalData) {
    out[idx++] = pads[p];
    p ^= 1;
  }
  return out;
}

function buildCodewords(bytes: Uint8Array, version: number): Uint8Array {
  const data = encodeDataBytes(bytes, version);
  const [, ecLen, g1Count, g1Size, g2Count, g2Size] = EC_M_TABLE[version];
  const dataBlocks: Uint8Array[] = [];
  const ecBlocks: Uint8Array[] = [];
  let offset = 0;
  for (let i = 0; i < g1Count; i++) {
    const blk = data.subarray(offset, offset + g1Size);
    dataBlocks.push(blk);
    ecBlocks.push(rsRemainder(blk, ecLen));
    offset += g1Size;
  }
  for (let i = 0; i < g2Count; i++) {
    const blk = data.subarray(offset, offset + g2Size);
    dataBlocks.push(blk);
    ecBlocks.push(rsRemainder(blk, ecLen));
    offset += g2Size;
  }

  const maxData = Math.max(g1Size, g2Size);
  const out: number[] = [];
  for (let i = 0; i < maxData; i++) {
    for (const blk of dataBlocks) {
      if (i < blk.length) out.push(blk[i]);
    }
  }
  for (let i = 0; i < ecLen; i++) {
    for (const ec of ecBlocks) out.push(ec[i]);
  }
  return new Uint8Array(out);
}

function formatBits(mask: number): number {
  // EC Level M = 00
  const data = mask & 0b111;
  let rem = data << 10;
  for (let i = 4; i >= 0; i--) {
    if (rem & (1 << (i + 10))) rem ^= 0x537 << i;
  }
  return ((data << 10) | rem) ^ 0x5412;
}

function versionBits(version: number): number {
  let rem = version << 12;
  for (let i = 5; i >= 0; i--) {
    if (rem & (1 << (i + 12))) rem ^= 0x1f25 << i;
  }
  return (version << 12) | rem;
}

function drawFinder(grid: Uint8Array, fixed: Uint8Array, size: number, r0: number, c0: number) {
  for (let dr = -1; dr <= 7; dr++) {
    for (let dc = -1; dc <= 7; dc++) {
      const r = r0 + dr;
      const c = c0 + dc;
      if (r < 0 || r >= size || c < 0 || c >= size) continue;
      const on =
        dr >= 0 &&
        dr <= 6 &&
        dc >= 0 &&
        dc <= 6 &&
        (dr === 0 || dr === 6 || dc === 0 || dc === 6 || (dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4));
      grid[r * size + c] = on ? 1 : 0;
      fixed[r * size + c] = 1;
    }
  }
}

function drawAlignment(grid: Uint8Array, fixed: Uint8Array, size: number, cr: number, cc: number) {
  for (let dr = -2; dr <= 2; dr++) {
    for (let dc = -2; dc <= 2; dc++) {
      const r = cr + dr;
      const c = cc + dc;
      const on = Math.max(Math.abs(dr), Math.abs(dc)) !== 1;
      grid[r * size + c] = on ? 1 : 0;
      fixed[r * size + c] = 1;
    }
  }
}

function applyMaskBit(mask: number, r: number, c: number): boolean {
  switch (mask) {
    case 0: return (r + c) % 2 === 0;
    case 1: return r % 2 === 0;
    case 2: return c % 3 === 0;
    case 3: return (r + c) % 3 === 0;
    case 4: return (Math.floor(r / 2) + Math.floor(c / 3)) % 2 === 0;
    case 5: return ((r * c) % 2) + ((r * c) % 3) === 0;
    case 6: return (((r * c) % 2) + ((r * c) % 3)) % 2 === 0;
    default: return (((r + c) % 2) + ((r * c) % 3)) % 2 === 0;
  }
}

function writeFormat(grid: Uint8Array, size: number, mask: number) {
  const bits = formatBits(mask);
  for (let i = 0; i < 15; i++) {
    // Bit 14 (MSB) starts at top-left (8, 0) / bottom-left (size - 1, 8); bit 0 (LSB) ends at (8, size - 1) / (0, 8).
    const bit = (bits >>> (14 - i)) & 1;

    // Horizontal copy across row 8
    if (i < 6) grid[8 * size + i] = bit;
    else if (i === 6) grid[8 * size + 7] = bit;
    else grid[8 * size + (size - 15 + i)] = bit;

    // Vertical copy along column 8
    if (i < 7) grid[(size - 1 - i) * size + 8] = bit;
    else if (i === 7) grid[8 * size + 8] = bit;
    else if (i === 8) grid[7 * size + 8] = bit;
    else grid[(14 - i) * size + 8] = bit;
  }
}

function penaltyScore(grid: Uint8Array, size: number): number {
  let score = 0;

  // Rule 1: 5+ consecutive same-color modules in row/col
  for (let r = 0; r < size; r++) {
    let runColor = grid[r * size];
    let runLen = 1;
    for (let c = 1; c < size; c++) {
      const v = grid[r * size + c];
      if (v === runColor) {
        runLen++;
        if (runLen === 5) score += 3;
        else if (runLen > 5) score += 1;
      } else {
        runColor = v;
        runLen = 1;
      }
    }
  }
  for (let c = 0; c < size; c++) {
    let runColor = grid[c];
    let runLen = 1;
    for (let r = 1; r < size; r++) {
      const v = grid[r * size + c];
      if (v === runColor) {
        runLen++;
        if (runLen === 5) score += 3;
        else if (runLen > 5) score += 1;
      } else {
        runColor = v;
        runLen = 1;
      }
    }
  }

  // Rule 2: 2x2 blocks of same color
  for (let r = 0; r < size - 1; r++) {
    for (let c = 0; c < size - 1; c++) {
      const v = grid[r * size + c];
      if (
        v === grid[r * size + c + 1] &&
        v === grid[(r + 1) * size + c] &&
        v === grid[(r + 1) * size + c + 1]
      ) {
        score += 3;
      }
    }
  }

  // Rule 3: 1:1:3:1:1 finder-like patterns
  const matchPattern = (get: (i: number) => number, len: number) => {
    let count = 0;
    for (let i = 0; i <= len - 11; i++) {
      const a =
        get(i) === 1 && get(i + 1) === 0 && get(i + 2) === 1 && get(i + 3) === 1 &&
        get(i + 4) === 1 && get(i + 5) === 0 && get(i + 6) === 1 &&
        get(i + 7) === 0 && get(i + 8) === 0 && get(i + 9) === 0 && get(i + 10) === 0;
      const b =
        get(i) === 0 && get(i + 1) === 0 && get(i + 2) === 0 && get(i + 3) === 0 &&
        get(i + 4) === 1 && get(i + 5) === 0 && get(i + 6) === 1 &&
        get(i + 7) === 1 && get(i + 8) === 1 && get(i + 9) === 0 && get(i + 10) === 1;
      if (a || b) count++;
    }
    return count * 40;
  };
  for (let r = 0; r < size; r++) score += matchPattern((i) => grid[r * size + i], size);
  for (let c = 0; c < size; c++) score += matchPattern((i) => grid[i * size + c], size);

  // Rule 4: dark module ratio deviation from 50%
  let dark = 0;
  for (let i = 0; i < grid.length; i++) dark += grid[i];
  const pct = (dark * 100) / (size * size);
  const prev5 = Math.floor(pct / 5) * 5;
  const next5 = prev5 + 5;
  score += Math.min(Math.abs(prev5 - 50), Math.abs(next5 - 50)) * 2;
  return score;
}

export function encodeQrCode(text: string): QrMatrix {
  const bytes = new TextEncoder().encode(text);
  let version = 0;
  for (let v = 1; v < EC_M_TABLE.length; v++) {
    const headerBits = 4 + (v <= 9 ? 8 : 16);
    const maxBytes = Math.floor((EC_M_TABLE[v][0] * 8 - headerBits) / 8);
    if (bytes.length <= maxBytes) {
      version = v;
      break;
    }
  }
  if (!version) {
    throw new Error("Configuration is too long for QR encoding.");
  }

  const size = version * 4 + 17;
  const base = new Uint8Array(size * size);
  const fixed = new Uint8Array(size * size);

  drawFinder(base, fixed, size, 0, 0);
  drawFinder(base, fixed, size, 0, size - 7);
  drawFinder(base, fixed, size, size - 7, 0);

  // Timing patterns
  for (let i = 8; i < size - 8; i++) {
    const on = i % 2 === 0 ? 1 : 0;
    base[6 * size + i] = on;
    fixed[6 * size + i] = 1;
    base[i * size + 6] = on;
    fixed[i * size + 6] = 1;
  }

  // Alignment patterns (skip the 3 finder corners, but draw over timing lines)
  const align = ALIGNMENT_POSITIONS[version];
  for (const r of align) {
    for (const c of align) {
      if ((r <= 8 && c <= 8) || (r <= 8 && c >= size - 8) || (r >= size - 8 && c <= 8)) continue;
      drawAlignment(base, fixed, size, r, c);
    }
  }

  // Reserve format info + dark module
  for (let i = 0; i < 9; i++) {
    if (i !== 6) {
      fixed[8 * size + i] = 1;
      fixed[i * size + 8] = 1;
    }
  }
  for (let i = 0; i < 8; i++) {
    fixed[8 * size + (size - 1 - i)] = 1;
    fixed[(size - 1 - i) * size + 8] = 1;
  }
  base[(size - 8) * size + 8] = 1;
  fixed[(size - 8) * size + 8] = 1;

  // Version info (versions >= 7)
  if (version >= 7) {
    const vbits = versionBits(version);
    for (let i = 0; i < 18; i++) {
      const bit = (vbits >>> i) & 1;
      const r = Math.floor(i / 3);
      const c = size - 11 + (i % 3);
      base[r * size + c] = bit;
      fixed[r * size + c] = 1;
      base[c * size + r] = bit;
      fixed[c * size + r] = 1;
    }
  }

  // Place data + EC codewords in zig-zag order
  const codewords = buildCodewords(bytes, version);
  let bitIdx = 0;
  const totalBits = codewords.length * 8;
  let dir = -1;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let step = 0; step < size; step++) {
      const r = dir < 0 ? size - 1 - step : step;
      for (let dc = 0; dc < 2; dc++) {
        const c = right - dc;
        if (fixed[r * size + c]) continue;
        let bit = 0;
        if (bitIdx < totalBits) {
          bit = (codewords[bitIdx >> 3] >>> (7 - (bitIdx & 7))) & 1;
          bitIdx++;
        }
        base[r * size + c] = bit;
      }
    }
    dir = -dir;
  }

  let bestGrid = base;
  let bestScore = Infinity;
  for (let mask = 0; mask < 8; mask++) {
    const cand = new Uint8Array(base);
    for (let r = 0; r < size; r++) {
      for (let c = 0; c < size; c++) {
        if (!fixed[r * size + c] && applyMaskBit(mask, r, c)) {
          cand[r * size + c] ^= 1;
        }
      }
    }
    writeFormat(cand, size, mask);
    const s = penaltyScore(cand, size);
    if (s < bestScore) {
      bestScore = s;
      bestGrid = cand;
    }
  }

  return { version, size, modules: bestGrid };
}

/** Build a single merged SVG path covering every dark module with a 4-module margin. */
export function qrSvgPath(matrix: QrMatrix, quiet = 4): string {
  const { size, modules } = matrix;
  const parts: string[] = [];
  for (let r = 0; r < size; r++) {
    let c = 0;
    while (c < size) {
      if (!modules[r * size + c]) {
        c++;
        continue;
      }
      const start = c;
      while (c < size && modules[r * size + c]) c++;
      const w = c - start;
      parts.push(`M${start + quiet} ${r + quiet}h${w}v1h-${w}z`);
    }
  }
  return parts.join("");
}
