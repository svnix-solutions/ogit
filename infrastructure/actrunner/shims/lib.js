// Shared code of ogit/upload-artifact and ogit/download-artifact: GitHub's
// artifact service (v4) without @actions/artifact's GitHub-host check.
// Node 20 standard library only.
"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const zlib = require("zlib");
const crypto = require("crypto");
const { pipeline } = require("stream/promises");
const { Readable, Transform } = require("stream");

// --- workflow commands -------------------------------------------------------

function input(name, fallback = "") {
  const v = process.env[`INPUT_${name.replace(/ /g, "_").toUpperCase()}`];
  return v === undefined || v.trim() === "" ? fallback : v.trim();
}

function boolInput(name, fallback) {
  const v = input(name, String(fallback)).toLowerCase();
  return v === "true" || v === "1" || v === "yes";
}

function setOutput(name, value) {
  const file = process.env.GITHUB_OUTPUT;
  if (file) fs.appendFileSync(file, `${name}=${value}\n`);
  else console.log(`::set-output name=${name}::${value}`);
}

const info = (m) => console.log(m);
const warning = (m) => console.log(`::warning::${m}`);

function fail(e) {
  console.log(`::error::${e && e.message ? e.message : e}`);
  process.exitCode = 1;
}

// --- the artifact service -----------------------------------------------------

function runtime() {
  const url = process.env.ACTIONS_RESULTS_URL || process.env.ACTIONS_RUNTIME_URL;
  const token = process.env.ACTIONS_RUNTIME_TOKEN;
  if (!url || !token) throw new Error("ACTIONS_RESULTS_URL and ACTIONS_RUNTIME_TOKEN are missing: is this running in an oGit runner?");
  // The run is in the token's scp claim: Actions.Results:<run>:<job>.
  const claims = JSON.parse(Buffer.from(token.split(".")[1], "base64url").toString());
  const scope = String(claims.scp || "").split(" ").find((s) => s.startsWith("Actions.Results:"));
  if (!scope) throw new Error("the runtime token names no run");
  const [, run, job] = scope.split(":");
  return { url: url.replace(/\/?$/, "/"), token, run, job };
}

async function twirp(method, body) {
  const rt = runtime();
  const res = await fetch(`${rt.url}twirp/github.actions.results.api.v1.ArtifactService/${method}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${rt.token}` },
    body: JSON.stringify({ workflow_run_backend_id: rt.run, workflow_job_run_backend_id: rt.job, ...body }),
  });
  const text = await res.text();
  let data = {};
  try {
    data = JSON.parse(text);
  } catch {}
  if (!res.ok) {
    const err = new Error(`${method}: ${data.msg || text || res.statusText}`);
    err.status = res.status;
    throw err;
  }
  return data;
}

// --- crc32 ----------------------------------------------------------------------

const CRC = new Int32Array(256).map((_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c;
});

function crc32(crc, buf) {
  crc = ~crc;
  for (let i = 0; i < buf.length; i++) crc = CRC[(crc ^ buf[i]) & 0xff] ^ (crc >>> 8);
  return ~crc >>> 0;
}

// --- zip ------------------------------------------------------------------------

function dosTime(d) {
  return ((d.getHours() << 11) | (d.getMinutes() << 5) | (d.getSeconds() >> 1)) & 0xffff;
}

function dosDate(d) {
  return (((d.getFullYear() - 1980) << 9) | ((d.getMonth() + 1) << 5) | d.getDate()) & 0xffff;
}

// writeZip streams files ([{abs, name}]) into a zip at out (deflate, or
// store with level 0), with data descriptors; returns its size.
async function writeZip(files, out, level) {
  const fd = fs.openSync(out, "w");
  let offset = 0;
  const write = (buf) => {
    fs.writeSync(fd, buf);
    offset += buf.length;
  };
  const central = [];
  try {
    for (const f of files) {
      const name = Buffer.from(f.name.split(path.sep).join("/"), "utf8");
      const stat = fs.statSync(f.abs);
      const method = level === 0 ? 0 : 8;
      const time = dosTime(stat.mtime), date = dosDate(stat.mtime);
      const local = Buffer.alloc(30);
      local.writeUInt32LE(0x04034b50, 0);
      local.writeUInt16LE(20, 4);
      local.writeUInt16LE(0x0808, 6); // data descriptor, UTF-8 names
      local.writeUInt16LE(method, 8);
      local.writeUInt16LE(time, 10);
      local.writeUInt16LE(date, 12);
      local.writeUInt16LE(name.length, 26);
      const headerAt = offset;
      write(local);
      write(name);
      let crc = 0, size = 0, compressed = 0;
      const count = new Transform({
        transform(chunk, _, cb) {
          crc = crc32(crc, chunk);
          size += chunk.length;
          cb(null, chunk);
        },
      });
      const sink = new (require("stream").Writable)({
        write(chunk, _, cb) {
          compressed += chunk.length;
          write(chunk);
          cb();
        },
      });
      const stages = [fs.createReadStream(f.abs), count];
      if (method === 8) stages.push(zlib.createDeflateRaw({ level: level < 0 ? 6 : level }));
      await pipeline(...stages, sink);
      if (size > 0xfffffffe || offset > 0xfffffffe) throw new Error("artifacts over 4 GB are not supported");
      const desc = Buffer.alloc(16);
      desc.writeUInt32LE(0x08074b50, 0);
      desc.writeUInt32LE(crc, 4);
      desc.writeUInt32LE(compressed, 8);
      desc.writeUInt32LE(size, 12);
      write(desc);
      const entry = Buffer.alloc(46);
      entry.writeUInt32LE(0x02014b50, 0);
      entry.writeUInt16LE(0x0314, 4); // made by: unix
      entry.writeUInt16LE(20, 6);
      entry.writeUInt16LE(0x0808, 8);
      entry.writeUInt16LE(method, 10);
      entry.writeUInt16LE(time, 12);
      entry.writeUInt16LE(date, 14);
      entry.writeUInt32LE(crc, 16);
      entry.writeUInt32LE(compressed, 20);
      entry.writeUInt32LE(size, 24);
      entry.writeUInt16LE(name.length, 28);
      entry.writeUInt32LE(((stat.mode & 0o777) << 16) >>> 0, 38);
      entry.writeUInt32LE(headerAt, 42);
      central.push(Buffer.concat([entry, name]));
    }
    const dirAt = offset;
    for (const c of central) write(c);
    const end = Buffer.alloc(22);
    end.writeUInt32LE(0x06054b50, 0);
    end.writeUInt16LE(central.length, 8);
    end.writeUInt16LE(central.length, 10);
    end.writeUInt32LE(offset - dirAt, 12);
    end.writeUInt32LE(dirAt, 16);
    write(end);
  } finally {
    fs.closeSync(fd);
  }
  return offset;
}

// extractZip writes a zip's files under dir (refusing paths that leave it).
async function extractZip(file, dir) {
  const fd = fs.openSync(file, "r");
  try {
    const size = fs.fstatSync(fd).size;
    const tail = Buffer.alloc(Math.min(size, 65557));
    fs.readSync(fd, tail, 0, tail.length, size - tail.length);
    const endAt = tail.lastIndexOf(Buffer.from([0x50, 0x4b, 0x05, 0x06]));
    if (endAt < 0) throw new Error("not a zip file");
    const count = tail.readUInt16LE(endAt + 10);
    let at = tail.readUInt32LE(endAt + 16);
    const root = path.resolve(dir);
    for (let i = 0; i < count; i++) {
      const h = Buffer.alloc(46);
      fs.readSync(fd, h, 0, 46, at);
      if (h.readUInt32LE(0) !== 0x02014b50) throw new Error("corrupt zip directory");
      const method = h.readUInt16LE(10), compressed = h.readUInt32LE(20);
      const nameLen = h.readUInt16LE(28), extraLen = h.readUInt16LE(30), commentLen = h.readUInt16LE(32);
      const mode = h.readUInt32LE(38) >>> 16, local = h.readUInt32LE(42);
      const nameBuf = Buffer.alloc(nameLen);
      fs.readSync(fd, nameBuf, 0, nameLen, at + 46);
      at += 46 + nameLen + extraLen + commentLen;
      const name = nameBuf.toString("utf8");
      const target = path.resolve(root, name);
      if (target !== root && !target.startsWith(root + path.sep)) throw new Error(`refusing ${name}: outside the target directory`);
      if (name.endsWith("/")) {
        fs.mkdirSync(target, { recursive: true });
        continue;
      }
      const lh = Buffer.alloc(30);
      fs.readSync(fd, lh, 0, 30, local);
      const dataAt = local + 30 + lh.readUInt16LE(26) + lh.readUInt16LE(28);
      fs.mkdirSync(path.dirname(target), { recursive: true });
      const stages = compressed === 0 ? [Readable.from([])] : [fs.createReadStream(null, { fd, start: dataAt, end: dataAt + compressed - 1, autoClose: false })];
      if (method === 8) stages.push(zlib.createInflateRaw());
      else if (method !== 0) throw new Error(`${name}: unsupported compression ${method}`);
      await pipeline(...stages, fs.createWriteStream(target));
      if (mode & 0o111) fs.chmodSync(target, mode & 0o777);
    }
  } finally {
    fs.closeSync(fd);
  }
}

// --- files ----------------------------------------------------------------------

function globToRegExp(glob) {
  let re = "";
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i];
    if (c === "*") {
      if (glob[i + 1] === "*") {
        re += "(?:.*)";
        i++;
        if (glob[i + 1] === "/") i++;
      } else re += "[^/]*";
    } else if (c === "?") re += "[^/]";
    else re += c.replace(/[.+^${}()|[\]\\]/g, "\\$&");
  }
  return new RegExp(`^${re}$`);
}

function walk(dir, out) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.isFile()) out.push(p);
  }
  return out;
}

const hasGlob = (s) => /[*?[]/.test(s);

// findFiles resolves upload-artifact's path input: files, directories and
// globs (one per line, "!" excludes); names are relative to the paths'
// common ancestor.
function findFiles(spec, includeHidden) {
  const ws = process.env.GITHUB_WORKSPACE || process.cwd();
  const lines = spec.split("\n").map((l) => l.trim()).filter(Boolean);
  const includes = lines.filter((l) => !l.startsWith("!"));
  const excludes = lines.filter((l) => l.startsWith("!")).map((l) => globToRegExp(path.resolve(ws, l.slice(1)).split(path.sep).join("/")));
  const found = new Map();
  const roots = [];
  for (const l of includes) {
    const abs = path.resolve(ws, l);
    if (hasGlob(l)) {
      const parts = abs.split(path.sep);
      const base = parts.slice(0, parts.findIndex(hasGlob)).join(path.sep) || path.sep;
      roots.push(base);
      if (!fs.existsSync(base)) continue;
      const re = globToRegExp(abs.split(path.sep).join("/"));
      for (const f of walk(base, [])) if (re.test(f.split(path.sep).join("/"))) found.set(f, true);
    } else if (fs.existsSync(abs) && fs.statSync(abs).isDirectory()) {
      roots.push(abs);
      for (const f of walk(abs, [])) found.set(f, true);
    } else if (fs.existsSync(abs)) {
      roots.push(path.dirname(abs));
      found.set(abs, true);
    }
  }
  let root = roots[0] || ws;
  for (const r of roots) while (r !== root && !r.startsWith(root + path.sep)) root = path.dirname(root);
  const files = [];
  for (const abs of found.keys()) {
    const posix = abs.split(path.sep).join("/");
    if (excludes.some((re) => re.test(posix))) continue;
    const name = path.relative(root, abs);
    if (!includeHidden && name.split(path.sep).some((p) => p.startsWith("."))) continue;
    files.push({ abs, name });
  }
  return files.sort((a, b) => a.name.localeCompare(b.name));
}

function tempFile(suffix) {
  return path.join(process.env.RUNNER_TEMP || os.tmpdir(), `ogit-artifact-${crypto.randomBytes(6).toString("hex")}${suffix}`);
}

async function sha256(file) {
  const h = crypto.createHash("sha256");
  await pipeline(fs.createReadStream(file), h);
  return h.digest("hex");
}

async function download(url, headers, to) {
  const res = await fetch(url, { headers, redirect: "follow" });
  if (!res.ok) throw new Error(`download failed: ${res.status} ${res.statusText}`);
  await pipeline(Readable.fromWeb(res.body), fs.createWriteStream(to));
}

module.exports = { input, boolInput, setOutput, info, warning, fail, twirp, runtime, writeZip, extractZip, findFiles, globToRegExp, tempFile, sha256, download };
