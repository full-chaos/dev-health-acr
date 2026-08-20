import { mkdir, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";

function value(name) {
    const found = process.env[name];
    if (!found) throw new Error(`${name} is required`);
    return found;
}

// optionalValue: SVS_TASK_REFERENCE flows into the live request as `task_ref`, which
// work_items.v1/work_item_dependencies.v1/work_graph.v1 use as an exact-match filter server
// side (see internal/contextpacket/source_queries.go) -- a non-empty value that does not match
// a seeded work item or work-graph edge silently drops those sources to "unavailable", exactly
// like leaving the field blank does for a real operator with no specific task in mind. The
// direct-HTTP/MCP paths this check is compared against never set task_ref at all, so the
// browser form must not force a value either.
function optionalValue(name) {
    return process.env[name] ?? "";
}

const baseUrl = value("SVS_WEB_URL");
const email = value("SVS_WEB_EMAIL");
const password = value("SVS_WEB_PASSWORD");
const goal = value("SVS_GOAL");
const repository = value("SVS_REPOSITORY");
const branch = value("SVS_BRANCH");
const taskReference = optionalValue("SVS_TASK_REFERENCE");
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

// captureRepositoryPickerState inspects the live <select> in the DOM rather than
// inferring anything from a later selectOption timeout: it answers "what did the
// control actually contain" directly, distinguishing a genuinely empty/disabled
// picker from one that has options the requested repository just isn't among.
async function captureRepositoryPickerState() {
    try {
        const locator = page.getByLabel("Repository");
        const count = await locator.count();
        if (count === 0) return { found: false };
        return await locator.first().evaluate((element) => ({
            found: true,
            tagName: element.tagName,
            disabled: element.disabled,
            optionCount: element.options.length,
            options: Array.from(element.options).map((option) => ({
                value: option.value,
                text: option.textContent,
            })),
        }));
    } catch (error) {
        return { found: false, captureError: String(error) };
    }
}

// captureRepositoryCatalogResponse re-issues the same request the Explorer's own
// client-side fetch makes, from inside the authenticated page context (reusing its
// session cookies), so a failure artifact can distinguish an "error" catalog.kind
// from a genuinely empty {repositories: []} from a populated list that simply
// doesn't contain the expected repository -- without guessing from the DOM alone.
async function captureRepositoryCatalogResponse() {
    try {
        return await page.evaluate(async () => {
            const response = await fetch("/api/agent-context/repositories", {
                headers: { Accept: "application/json" },
            });
            const text = await response.text();
            let body = text;
            try {
                body = JSON.parse(text);
            } catch {
                // leave body as raw text if it is not JSON
            }
            return { status: response.status, body };
        });
    } catch (error) {
        return { captureError: String(error) };
    }
}

// captureFailureArtifacts is strictly additive: it runs only from the catch block
// below, immediately before the original error is rethrown, and a failure inside
// this function must never mask or replace that original error.
async function captureFailureArtifacts(error) {
    const dir = dirname(screenshotOutput);
    await mkdir(dir, { recursive: true });
    const failureScreenshot = resolve(dir, "failure.png");
    const failureDom = resolve(dir, "failure-dom.html");
    const failurePicker = resolve(dir, "failure-picker.json");
    const failureCatalog = resolve(dir, "failure-catalog.json");
    const failureSummary = resolve(dir, "failure-summary.json");

    const [picker, catalog] = await Promise.all([
        captureRepositoryPickerState(),
        captureRepositoryCatalogResponse(),
    ]);

    const outcomes = await Promise.allSettled([
        page.screenshot({ path: failureScreenshot, fullPage: true }),
        page.content().then((html) => writeFile(failureDom, html)),
        writeFile(failurePicker, `${JSON.stringify(picker, null, 2)}\n`),
        writeFile(failureCatalog, `${JSON.stringify(catalog, null, 2)}\n`),
        writeFile(
            failureSummary,
            `${JSON.stringify(
                {
                    error: String(error?.message ?? error),
                    url: page.url(),
                    authRequestUrls,
                    directAcrRequests,
                    browserErrors,
                },
                null,
                2,
            )}\n`,
        ),
    ]);
    const failedCaptures = outcomes
        .map((outcome, index) => ({ outcome, index }))
        .filter(({ outcome }) => outcome.status === "rejected");
    if (failedCaptures.length > 0) {
        const labels = ["screenshot", "dom", "picker", "catalog", "summary"];
        for (const { outcome, index } of failedCaptures) {
            console.error(`failure artifact capture (${labels[index]}) failed: ${outcome.reason}`);
        }
    }
}

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
    // /agent-context/context-packet is now a compatibility alias that redirects a superuser to
    // /superadmin/context-fabric/validation, whose page wrapper renders its own "Context Fabric
    // Validation" h1 above the Explorer widget's "Context Fabric" h1 -- both level 1, so a
    // substring match on "Context Fabric" resolves to both and Playwright's strict mode
    // rejects it. `exact: true` pins this to the Explorer widget's own heading, matching what
    // this locator found (uniquely) before that page move.
    await page.getByRole("heading", { name: "Context Fabric", level: 1, exact: true }).waitFor();
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
} catch (error) {
    await captureFailureArtifacts(error).catch((captureError) => {
        console.error(`failure artifact capture itself failed: ${captureError}`);
    });
    throw error;
} finally {
    await browser.close();
}
