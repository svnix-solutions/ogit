// ogit/download-artifact: actions/download-artifact@v4's inputs and
// outputs, for oGit's artifact service: this run's artifacts, or another
// run's with run-id and github-token.
"use strict";

const fs = require("fs");
const path = require("path");
const lib = require("./lib.js");

async function main() {
  const name = lib.input("name");
  const pattern = lib.input("pattern");
  const merge = lib.boolInput("merge-multiple", false);
  const ws = process.env.GITHUB_WORKSPACE || process.cwd();
  const dest = path.resolve(ws, lib.input("path", ws).replace(/^~(?=$|\/)/, process.env.HOME || "~"));
  const runId = lib.input("run-id");
  const token = lib.input("github-token");

  // What to download: [{name, fetch(to)}].
  let artifacts;
  if (runId) {
    if (!token) throw new Error("github-token is required with run-id");
    const repo = lib.input("repository", process.env.GITHUB_REPOSITORY);
    const api = (process.env.GITHUB_API_URL || `${process.env.GITHUB_SERVER_URL}/api/v3`).replace(/\/$/, "");
    const headers = { Authorization: `token ${token}`, Accept: "application/vnd.github+json" };
    const q = name ? `?name=${encodeURIComponent(name)}` : "";
    const res = await fetch(`${api}/repos/${repo}/actions/runs/${runId}/artifacts${q}`, { headers });
    if (!res.ok) throw new Error(`listing the artifacts of run ${runId}: ${res.status} ${await res.text()}`);
    artifacts = (await res.json()).artifacts.map((a) => ({
      name: a.name,
      fetch: (to) => lib.download(`${api}/repos/${repo}/actions/artifacts/${a.id}/zip`, headers, to),
    }));
  } else {
    const body = name ? { name_filter: name } : {};
    artifacts = ((await lib.twirp("ListArtifacts", body)).artifacts || []).map((a) => ({
      name: a.name,
      fetch: async (to) => {
        const { signedUrl } = await lib.twirp("GetSignedArtifactURL", { name: a.name });
        await lib.download(signedUrl, {}, to);
      },
    }));
  }
  if (pattern && !name) {
    const re = lib.globToRegExp(pattern);
    artifacts = artifacts.filter((a) => re.test(a.name));
  }
  if (name && artifacts.length === 0) throw new Error(`Artifact not found for name: ${name}`);

  for (const a of artifacts) {
    const into = name || merge ? dest : path.join(dest, a.name);
    const zip = lib.tempFile(".zip");
    try {
      await a.fetch(zip);
      fs.mkdirSync(into, { recursive: true });
      await lib.extractZip(zip, into);
      lib.info(`Artifact ${a.name} downloaded to ${into}`);
    } finally {
      fs.rmSync(zip, { force: true });
    }
  }
  lib.info(`Total of ${artifacts.length} artifact(s) downloaded`);
  lib.setOutput("download-path", dest);
}

main().catch(lib.fail);
