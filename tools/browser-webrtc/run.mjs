// Disposable native browser fixture, not a shipped client.
import { createServer } from "node:http";
import { mkdtemp, rm, writeFile,readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, resolve, join } from "node:path";
import { fileURLToPath } from "node:url";
import { randomBytes } from "node:crypto";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { build } from "esbuild";
import { exportSPKI, generateKeyPair, SignJWT } from "jose";
import { chromium } from "playwright-core";
const here = dirname(fileURLToPath(import.meta.url)), root = resolve(here, "../..");
const temp = await mkdtemp(join(tmpdir(), "qgramm-browser-"));
let browser, child, server;
let management, base, bootstrap;
const request = async (method, path, body) => {
  const res = await fetch(base + path, { method, headers: { Authorization: "Bearer " + management, "Content-Type": "application/json" }, body: JSON.stringify(body) });
  if (!res.ok) throw new Error("management " + method + " " + path + " HTTP " + res.status);
  return res.json();
};
try {
  const bundle = await build({ entryPoints: [join(here, "page.js")], bundle: true, write: false, format: "esm", platform: "browser" });
  const javascript = bundle.outputFiles[0].text;const pcm=await readFile(join(here,"pcm.js"),"utf8");
  server = createServer(async (req, res) => {
    try {
      res.setHeader("Cache-Control", "no-store");
      if (req.method === "GET" && req.url === "/") {
        res.setHeader("Content-Type", "text/html");
        res.end('<!doctype html><html><meta charset="utf-8"><title>QGramm native browser acceptance</title><h1>Temporary native browser media acceptance</h1><p>Synthetic oscillator and canvas streams; no camera or microphone access.</p><button id="run">Run browser checks</button><div id="media"></div><pre id="results">Ready</pre><script type="module" src="/page.js"><\/script></html>');
        return;
      }
      if (req.method === "GET" && req.url === "/page.js") {
        res.setHeader("Content-Type", "text/javascript");
        res.end(javascript);
        return;
      }
      if(req.method === "GET" && req.url === "/pcm.js"){res.setHeader("Content-Type","text/javascript");res.end(pcm);return}
if (req.method === "GET" && req.url === "/bootstrap") {
        res.setHeader("Content-Type", "application/json");
        res.end(JSON.stringify(bootstrap));
        return;
      }
      if (req.method === "POST" && req.url === "/register") {
        if (req.headers.origin !== `http://127.0.0.1:${server.address().port}`) {
          res.writeHead(403).end();
          return;
        }
        let bytes = 0;
        const chunks = [];
        for await (const part of req) {
          bytes += part.length;
          if (bytes > 8192) {
            res.writeHead(413).end();
            return;
          }
          ;
          chunks.push(part);
        }
        ;
        const keys = JSON.parse(Buffer.concat(chunks));
        if (!Array.isArray(keys) || keys.length !== 2) throw new Error("invalid browser public keys");
        for (let i = 0; i < keys.length; i++) {
          const user = i === 0 ? "alice" : "bob";
          await request("PUT", "/management/v1/users/" + user, {});
          await request("PUT", `/management/v1/users/${user}/devices/${user}-browser`, { public_key: keys[i] });
        }
        ;
        res.setHeader("Content-Type", "application/json");
        res.end('{"registered":true}');
        return;
      }
      if (req.method === "POST" && req.url === "/chat") {
        if (req.headers.origin !== `http://127.0.0.1:${server.address().port}`) {
          res.writeHead(403).end();
          return;
        }
        ;
        let input = "";
        for await (const part of req) {
          input += part;
          if (input.length > 1024) {
            res.writeHead(413).end();
            return;
          }
        }
        ;
        const { id } = JSON.parse(input);
        if (!/^browser-(audio|video|mismatch)$/.test(id)) throw new Error("invalid chat id");
        await request("POST", "/management/v1/chats/direct", { id, mode: "basic", members: ["alice", "bob"] });
        res.setHeader("Content-Type", "application/json");
        res.end("{}");
        return;
      }
      res.writeHead(404).end();
    } catch {
      res.writeHead(500).end("fixture request failed");
    }
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  const origin = `http://127.0.0.1:${server.address().port}`;
  const probe = createServer();
  await new Promise((r) => probe.listen(0, "127.0.0.1", r));
  const port = probe.address().port;
  await new Promise((r) => probe.close(r));
  base = `http://127.0.0.1:${port}`;
  const { privateKey, publicKey } = await generateKeyPair("EdDSA", { extractable: true });
  const pem = await exportSPKI(publicKey);
  const der = Buffer.from(pem.split("\n").filter((x) => !x.includes("-----")).join(""), "base64");
  const publicRaw = der.subarray(der.length - 32);
  management = randomBytes(32).toString("base64");
  const config = join(temp, "calls.toml");
  await writeFile(config, `[server]
listen="127.0.0.1:${port}"
allow_insecure_loopback=true
origins=["${origin}"]
[storage]
path=${JSON.stringify(join(temp, "qg.db"))}
files=${JSON.stringify(join(temp, "files"))}
[features]
calls=true
[calls]
turn_urls=["turn:127.0.0.1:3478"]
turn_secret_env="QGRAMM_BROWSER_TURN"
`, { mode: 384 });
  const binary = process.env.QGRAMM_BROWSER_SERVER_BINARY || join(temp, "qgramm");
  if (!process.env.QGRAMM_BROWSER_SERVER_BINARY) try {
    execFileSync("go", ["run", "./cmd/qgramm-build", "build", "-config", config, "-out", binary], { cwd: root, stdio: ["ignore", "pipe", "pipe"], timeout: 12e4 });
  } catch (error) {
    throw new Error("calls artifact build failed: " + (error.stderr?.toString() || error.message));
  }
  child = spawn(binary, ["-config", config], { cwd: root, stdio: "ignore", env: { ...process.env, QGRAMM_TOKEN_PUBLIC_KEY: publicRaw.toString("base64"), QGRAMM_MASTER_KEY: randomBytes(32).toString("base64"), QGRAMM_HPKE_KEY: randomBytes(32).toString("base64"), QGRAMM_MANAGEMENT_SECRET: management, QGRAMM_BROWSER_TURN: randomBytes(32).toString("base64") } });
  let ready = false;
  for (let i = 0; i < 100; i++) {
    try {
      const r = await fetch(base + "/readyz");
      if (r.ok) {
        ready = true;
        break;
      }
    } catch {
    }
    ;
    await new Promise((r) => setTimeout(r, 50));
  }
  ;
  if (!ready) throw new Error("isolated calls binary did not become ready");
  const peers = [];
  for (const user of ["alice", "bob"]) {
    const token = await new SignJWT({ device_id: user + "-browser" }).setProtectedHeader({ alg: "EdDSA" }).setSubject(user).setIssuer("qgramm").setAudience("qgramm").setIssuedAt().setExpirationTime("10m").sign(privateKey);
    peers.push({ user, device: user + "-browser", token });
  }
  ;
  bootstrap = { base, peers };
  const executable = process.env.QGRAMM_BROWSER_EXECUTABLE || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
  browser = await chromium.launch({ executablePath: executable, headless: true, args: ["--autoplay-policy=no-user-gesture-required", "--disable-features=WebRtcHideLocalIpsWithMdns", "--allow-loopback-in-peer-connection", "--force-webrtc-ip-handling-policy=default"] });
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(origin);
  await page.locator("#run").click();
  await page.waitForFunction(() => window.finished === true, {}, { timeout: 1e5 });
  const result = await page.evaluate(() => ({ results: window.results, error: window.testError }));
  if (result.error) throw new Error(result.error);
  if (errors.length) throw new Error("browser page errors: " + errors.join("; "));
  console.log(JSON.stringify({ browser: (process.platform === "linux" ? "Chromium " : "Google Chrome ") + browser.version(), runtime: process.platform + "/" + process.arch, results: result.results }, null, 2));
  if(result.results.some(x=>x.accepted===false))throw new Error("native browser acceptance incomplete; see per-case evidence above");
} finally {
  if (browser) await browser.close();
  if (child) {
    const exit = once(child, "exit");
    child.kill("SIGTERM");
    await Promise.race([exit, new Promise((r) => setTimeout(r, 3e3))]);
    if (child.exitCode === null) child.kill("SIGKILL");
  }
  ;
  if (server) await new Promise((r) => server.close(r));
  await rm(temp, { recursive: true, force: true });
}
