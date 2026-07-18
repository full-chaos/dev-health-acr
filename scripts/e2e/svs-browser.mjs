import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

function value(name) {
    const found = process.env[name];
    if (!found) throw new Error(`${name} is required`);
    return found;
}

const baseUrl = value("SVS_WEB_URL");
const email = value("SVS_WEB_EMAIL");
const password = value("SVS_WEB_PASSWORD");
const goal = value("SVS_GOAL");
const repository = value("SVS_REPOSITORY");
const branch = value("SVS_BRANCH");
const taskReference = value("SVS_TASK_REFERENCE");
const packetOutput = resolve(value("SVS_BROWSER_PACKET"));
const evidenceOutput = resolve(value("SVS_BROWSER_EVIDENCE"));
const screenshotOutput = resolve(value("SVS_BROWSER_SCREENSHOT"));
const playwrightModule = value("SVS_PLAYWRIGHT_MODULE");

const playwright = await import(playwrightModule);
const chromium = playwright.chromium ?? playwright.default?.chromium;
if (!chromium) throw new Error("Playwright Chromium is unavailable");
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
const page = await context.newPage();
const directAcrRequests = [];
const authRequestUrls = [];
const browserErrors = [];
page.on("request", (request) => {
    if (new URL(request.url()).pathname.startsWith("/api/auth/")) {
        authRequestUrls.push(`${request.method()} ${request.url()}`);
    }
    if (request.url().includes("/api/v1/agent-context/")) directAcrRequests.push(request.url());
});
page.on("console", (message) => {
    if (message.type() === "error") browserErrors.push(message.text());
});
page.on("pageerror", (error) => browserErrors.push(error.message));

try {
    await page.goto(`${baseUrl}/auth/signin`, { waitUntil: "networkidle" });
    await page.getByLabel("Email").fill(email);
    await page.getByLabel("Password").fill(password);
    const signInResponse = page.waitForResponse(
        (response) =>
            new URL(response.url()).pathname === "/api/auth/callback/credentials" &&
            response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Sign In" }).click();
    let signInResult;
    try {
        signInResult = await signInResponse;
    } catch {
        throw new Error(`canonical account did not submit: ${authRequestUrls.join(" | ")} ${browserErrors.join(" | ")}`);
    }
    if (!signInResult.ok()) throw new Error("canonical account authentication failed");
    const signInDetails = `${signInResult.status()} ${signInResult.url()} ${await signInResult.text()}`;
    try {
        await page.waitForURL((url) => !url.pathname.startsWith("/auth/signin"));
    } catch {
        throw new Error(`canonical account did not navigate: ${signInDetails} ${await page.locator("body").innerText()}`);
    }

    await page.goto(`${baseUrl}/agent-context/context-packet`, { waitUntil: "networkidle" });
    await page.getByRole("heading", { name: "Context Fabric", level: 1 }).waitFor();
    await page.getByLabel(/Goal/).fill(goal);
    await page.getByLabel("Repository").selectOption(repository);
    await page.getByLabel("Branch or commit").fill(branch);
    await page.getByLabel("Task reference").fill(taskReference);

    const packetResponse = page.waitForResponse(
        (response) =>
            new URL(response.url()).pathname === "/api/agent-context/context-packets" &&
            response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Generate context" }).click();
    const packetResult = await packetResponse;
    const packet = await packetResult.json();
    if (packet.schema_version !== "context_packet.v1") {
        throw new Error(`browser packet contract mismatch: ${packetResult.status()} ${JSON.stringify(packet)}`);
    }
    await page.getByRole("heading", { name: goal }).waitFor();

    const evidenceResponse = page.waitForResponse(
        (response) =>
            new URL(response.url()).pathname.startsWith("/api/agent-context/evidence/") &&
            response.request().method() === "GET",
    );
    await page.getByRole("button", { name: "Open evidence" }).first().click();
    const evidence = await (await evidenceResponse).json();
    if (evidence.schema_version !== "expanded_evidence.v1") {
        throw new Error("browser evidence contract mismatch");
    }
    if (directAcrRequests.length !== 0) throw new Error("browser bypassed the server-owned ACR boundary");

    await Promise.all([mkdir(dirname(packetOutput), { recursive: true }), mkdir(dirname(evidenceOutput), { recursive: true })]);
    await Promise.all([
        writeFile(packetOutput, `${JSON.stringify(packet, null, 2)}\n`, { mode: 0o600 }),
        writeFile(evidenceOutput, `${JSON.stringify(evidence, null, 2)}\n`, { mode: 0o600 }),
        page.screenshot({ path: screenshotOutput, fullPage: true }),
    ]);
} finally {
    await browser.close();
}
