// ogit/upload-artifact: actions/upload-artifact@v4's inputs and outputs,
// for oGit's artifact service.
"use strict";

const fs = require("fs");
const lib = require("./lib.js");

async function main() {
  const name = lib.input("name", "artifact");
  const spec = lib.input("path");
  if (!spec) throw new Error("path is required");
  const ifNone = lib.input("if-no-files-found", "warn");
  const retention = parseInt(lib.input("retention-days", "0"), 10) || 0;
  const level = parseInt(lib.input("compression-level", "6"), 10);
  const files = lib.findFiles(spec, lib.boolInput("include-hidden-files", false));
  if (files.length === 0) {
    const msg = `No files were found with the provided path: ${spec}. No artifacts will be uploaded.`;
    if (ifNone === "error") throw new Error(msg);
    if (ifNone === "warn") lib.warning(msg);
    else lib.info(msg);
    return;
  }
  lib.info(`With the provided path, there will be ${files.length} file(s) uploaded`);
  if (lib.boolInput("overwrite", false)) {
    try {
      await lib.twirp("DeleteArtifact", { name });
      lib.info(`Deleted the existing artifact ${name}`);
    } catch (e) {
      if (e.status !== 404) throw e;
    }
  }

  const zip = lib.tempFile(".zip");
  try {
    const size = await lib.writeZip(files, zip, Number.isNaN(level) ? 6 : level);
    const body = { name, version: 4 };
    if (retention > 0) body.expires_at = new Date(Date.now() + retention * 86400000).toISOString();
    const created = await lib.twirp("CreateArtifact", body);
    const res = await fetch(created.signedUploadUrl, {
      method: "PUT",
      headers: { "Content-Type": "application/zip", "Content-Length": String(size) },
      body: fs.createReadStream(zip),
      duplex: "half",
    });
    if (!res.ok) throw new Error(`upload failed: ${res.status} ${(await res.text()) || res.statusText}`);
    const digest = await lib.sha256(zip);
    const done = await lib.twirp("FinalizeArtifact", { name, size: String(size), hash: `sha256:${digest}` });
    lib.info(`Artifact ${name} uploaded: ${size} bytes, id ${done.artifactId}`);
    lib.setOutput("artifact-id", done.artifactId);
    lib.setOutput("artifact-digest", digest);
    lib.setOutput("artifact-url", `${process.env.GITHUB_SERVER_URL}/${process.env.GITHUB_REPOSITORY}/actions`);
  } finally {
    fs.rmSync(zip, { force: true });
  }
}

main().catch(lib.fail);
