import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

function required(name) {
    const value = process.env[name];
    if (!value) throw new Error(`${name} is required`);
    return value;
}

const baseUrl = required("DEVICE_LOGIN_WEB_URL");
const email = required("DEVICE_LOGIN_WEB_EMAIL");
const password = required("DEVICE_LOGIN_WEB_PASSWORD");
const code = (await readFile(required("DEVICE_LOGIN_CODE_FILE"), "utf8")).trim();
const repository = required("DEVICE_LOGIN_REPOSITORY");
const artifacts = resolve(required("DEVICE_LOGIN_ARTIFACTS"));
const playwright = await import(required("DEVICE_LOGIN_PLAYWRIGHT_MODULE"));
const chromium = playwright.chromium ?? playwright.default?.chromium;

if (!chromium) throw new Error("Playwright Chromium is unavailable");
if (!/^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}$/.test(code)) {
    throw new Error("device code is malformed");
}

const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { height: 844, width: 1280 } });
const page = await context.newPage();
const browserErrors = [];
const failedRequests = [];
const responses = [];
const deviceRequests = [];

async function captureState(state) {
    for (const width of [375, 768, 1280]) {
        await page.setViewportSize({ height: 900, width });
        await page.screenshot({ path: resolve(artifacts, `device-login-${state}-${width}.png`), fullPage: true });
    }
}

function redactResponseBody(body) {
    return body
        .replace(/[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}/g, "REDACTED_DEVICE_CODE")
        .replace(/(fcacr|svc_acr)_[A-Za-z0-9_-]+/g, "REDACTED")
        .slice(0, 512);
}

async function requireDeviceSuccess(response, operation) {
    if (response.ok()) return;
    const body = redactResponseBody(await response.text());
    throw new Error(`device approval ${operation} failed: ${response.status()} ${body}`);
}

page.on("console", (message) => {
    if (message.type() === "error") browserErrors.push(message.text());
});
page.on("pageerror", (error) => browserErrors.push(error.message));
page.on("requestfailed", (request) => failedRequests.push(request.url()));
page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname !== "/api/acr/device") return;
    const body = request.postDataJSON();
    if (!body || typeof body !== "object" || Array.isArray(body)) {
        throw new Error("device request body is malformed");
    }
    const action = Reflect.get(body, "action");
    const scopes = Reflect.get(body, "repository_scopes");
    if ((action !== "preview" && action !== "approve") || url.search !== "") {
        throw new Error("device request has an invalid action or query string");
    }
    if (request.headers()["authorization"] !== undefined) {
        throw new Error("browser forwarded bearer authorization to device route");
    }
    deviceRequests.push({ action, scopes: Array.isArray(scopes) ? scopes : [] });
});
page.on("response", (response) => {
    const url = new URL(response.url());
    if (url.pathname === "/api/acr/device") {
        responses.push({ method: response.request().method(), path: url.pathname, status: response.status() });
    }
});

try {
    await page.goto(`${baseUrl}/auth/signin`, { waitUntil: "networkidle" });
    await page.getByLabel("Email").fill(email);
    await page.getByLabel("Password").fill(password);
    const signIn = page.waitForResponse(
        (response) =>
            new URL(response.url()).pathname === "/api/auth/callback/credentials" &&
            response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Sign In" }).click();
    if (!(await signIn).ok()) throw new Error("isolated web fixture authentication failed");
    await page.waitForURL((url) => !url.pathname.startsWith("/auth/signin"));
    await page.waitForLoadState("domcontentloaded");

    await page.goto(`${baseUrl}/acr/device`, { waitUntil: "networkidle" });
    await page.getByRole("heading", { name: "Approve device access" }).waitFor();
    await captureState("pending");

    const preview = page.waitForResponse(
        (response) =>
            new URL(response.url()).pathname === "/api/acr/device" &&
            response.request().method() === "POST",
    );
    await page.getByLabel("Verification code").fill(code);
    await page.getByRole("button", { name: "Preview request" }).click();
    await requireDeviceSuccess(await preview, "preview");
    await page.getByRole("heading", { name: "Review device access" }).waitFor();
    await page.setViewportSize({ height: 844, width: 768 });
    const repositoryChoice = page.getByLabel(repository);
    if (!(await repositoryChoice.isChecked())) throw new Error("bounded repository was not selected");
    if ((await page.getByRole("checkbox").count()) !== 1) throw new Error("approval exposed an unbounded repository choice");
    await captureState("review");

    const approval = page.waitForResponse(
        (response) =>
            new URL(response.url()).pathname === "/api/acr/device" &&
            response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Confirm" }).click();
    await requireDeviceSuccess(await approval, "approval");
    await page.getByRole("heading", { name: "Approval complete" }).waitFor();
    await captureState("success");

    await page.goto(`${baseUrl}/acr/device`, { waitUntil: "networkidle" });
    const replay = page.waitForResponse(
        (response) =>
            new URL(response.url()).pathname === "/api/acr/device" &&
            response.request().method() === "POST",
    );
    await page.getByLabel("Verification code").fill(code);
    await page.getByRole("button", { name: "Preview request" }).click();
    if ((await replay).status() !== 409) throw new Error("replayed device authorization did not fail closed");
    await page.getByRole("heading", { name: "Request not approved" }).waitFor();

    if (browserErrors.length !== 0 || failedRequests.length !== 0) {
        throw new Error("browser emitted console errors or failed requests");
    }
    if (responses.some((response) => response.status >= 500)) throw new Error("device route returned 5xx");
    if (deviceRequests.length !== 3 || deviceRequests[0].action !== "preview" || deviceRequests[1].action !== "approve") {
        throw new Error("device route sequence was not preview then approve with replay rejection");
    }
    if (deviceRequests[1].scopes.length !== 1 || deviceRequests[1].scopes[0] !== repository) {
        throw new Error("approved repository scope was not bounded to the selected repository");
    }
    await writeFile(
        resolve(artifacts, "device-login-network.json"),
        `${JSON.stringify({ browserErrors, deviceRequests, failedRequests, responses }, null, 2)}\n`,
        { mode: 0o600 },
    );
} finally {
    await browser.close();
}
