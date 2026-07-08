"use strict";

function readStdin() {
  return new Promise((resolve, reject) => {
    const chunks = [];
    process.stdin.setEncoding("utf8");
    process.stdin.on("data", (chunk) => chunks.push(chunk));
    process.stdin.on("end", () => resolve(chunks.join("")));
    process.stdin.on("error", reject);
  });
}

function toStringValue(value) {
  return value === undefined || value === null ? "" : String(value);
}

function normalizeSameSite(value) {
  const text = toStringValue(value).trim().toLowerCase();
  if (!text) {
    return "";
  }
  if (text === "strict") {
    return "Strict";
  }
  if (text === "none") {
    return "None";
  }
  if (text === "lax") {
    return "Lax";
  }
  return "";
}

function normalizeCookies(cookies, fallbackUrl) {
  if (!Array.isArray(cookies)) {
    return [];
  }
  const result = [];
  for (const cookie of cookies) {
    if (!cookie || typeof cookie !== "object") {
      continue;
    }
    const name = toStringValue(cookie.name).trim();
    if (!name) {
      continue;
    }
    const normalized = {
      name,
      value: toStringValue(cookie.value),
      path: toStringValue(cookie.path).trim() || "/",
      secure: cookie.secure !== false,
      httpOnly: !!cookie.httpOnly,
    };
    const sameSite = normalizeSameSite(cookie.sameSite);
    if (sameSite) {
      normalized.sameSite = sameSite;
    }
    const expires = Number(cookie.expires);
    if (Number.isFinite(expires) && expires > 0) {
      normalized.expires = expires;
    }
    const domain = toStringValue(cookie.domain).trim();
    if (domain) {
      normalized.domain = domain;
    } else {
      normalized.url = fallbackUrl;
    }
    result.push(normalized);
  }
  return result;
}

function parseProxy(proxyValue) {
  const text = toStringValue(proxyValue).trim();
  if (!text) {
    return undefined;
  }
  try {
    const parsed = new URL(text.includes("://") ? text : `http://${text}`);
    const proxy = { server: `${parsed.protocol}//${parsed.host}` };
    if (parsed.username) {
      proxy.username = decodeURIComponent(parsed.username);
    }
    if (parsed.password) {
      proxy.password = decodeURIComponent(parsed.password);
    }
    return proxy;
  } catch {
    return { server: text };
  }
}

function parseSentinelJson(value) {
  const text = toStringValue(value).trim();
  if (!text) {
    return {};
  }
  try {
    const parsed = JSON.parse(text);
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

function parseBrandEntries(value) {
  const text = toStringValue(value);
  const result = [];
  for (const match of text.matchAll(/"([^"]+)";v="([^"]+)"/g)) {
    const brand = toStringValue(match && match[1]).trim();
    const version = toStringValue(match && match[2]).trim();
    if (!brand || !version) {
      continue;
    }
    result.push({ brand, version });
  }
  return result;
}

function buildUserAgentMetadata(input) {
  const metadata = input && typeof input.userAgentMetadata === "object" ? input.userAgentMetadata : {};
  const brands = Array.isArray(metadata.brands) && metadata.brands.length > 0 ? metadata.brands : parseBrandEntries(input.secChUa);
  const fullVersion = toStringValue(metadata.fullVersion).trim() || "145.0.0.0";
  const fullVersionList = Array.isArray(metadata.fullVersionList) && metadata.fullVersionList.length > 0
    ? metadata.fullVersionList
    : parseBrandEntries(input.secChUaFullVersionList);
  return {
    brands,
    fullVersionList: fullVersionList.length > 0 ? fullVersionList : brands.map((item) => ({ brand: item.brand, version: fullVersion })),
    fullVersion,
    platform: toStringValue(metadata.platform).trim() || "Windows",
    platformVersion: toStringValue(metadata.platformVersion).trim() || "10.0.0",
    architecture: toStringValue(metadata.architecture).trim() || "x86",
    bitness: toStringValue(metadata.bitness).trim() || "64",
    model: toStringValue(metadata.model).trim(),
    mobile: !!metadata.mobile,
    wow64: !!metadata.wow64,
  };
}

function loadPlaywright() {
  try {
    return require("playwright");
  } catch (error) {
    const message = error && error.message ? error.message : String(error || "unknown");
    throw new Error(`playwright_load_failed:${message}`);
  }
}

function isSentinelReqUrl(value) {
  return toStringValue(value).includes("/backend-api/sentinel/req");
}

function parseResponseBody(bodyResult) {
  if (!bodyResult || typeof bodyResult !== "object") {
    return {};
  }
  let text = toStringValue(bodyResult.body);
  if (bodyResult.base64Encoded && text) {
    try {
      text = Buffer.from(text, "base64").toString("utf8");
    } catch {
      return {};
    }
  }
  return parseSentinelJson(text);
}

function challengeTokenFromHeaders(sentinelHeader, soHeader) {
  const sentinelPayload = parseSentinelJson(sentinelHeader);
  const soPayload = parseSentinelJson(soHeader);
  const sentinelChallenge = toStringValue(sentinelPayload.c).trim();
  const soChallenge = toStringValue(soPayload.c).trim();
  return {
    sentinelPayload,
    soPayload,
    challengeMismatch: !!(sentinelChallenge && soChallenge && sentinelChallenge !== soChallenge),
    challengeToken: sentinelChallenge || soChallenge,
  };
}

function validateRuntimeCheck(input, browserResult, tokenMeta, matchedReqPayload, matchedReqCount, matchedReqStatus) {
  if (!toStringValue(browserResult && browserResult.sentinelHeader).trim()) {
    throw new Error("sentinel_runtime_check_missing_token");
  }
  if (tokenMeta && tokenMeta.challengeMismatch) {
    throw new Error("sentinel_challenge_mismatch: header_challenge_mismatch");
  }
  const reqPayload = matchedReqPayload && typeof matchedReqPayload === "object" ? matchedReqPayload : {};
  if (Object.keys(reqPayload).length === 0) {
    throw new Error("sentinel_req_metadata_missing");
  }
  const challengeToken = toStringValue(tokenMeta && tokenMeta.challengeToken).trim();
  const reqChallengeToken = toStringValue(reqPayload.token).trim();
  if (!challengeToken || !reqChallengeToken) {
    throw new Error("sentinel_challenge_mismatch: missing_req_token");
  }
  if (Number(matchedReqCount) <= 0 || reqChallengeToken !== challengeToken) {
    throw new Error("sentinel_challenge_mismatch: req_token_unmatched");
  }
  if (Number(matchedReqStatus) >= 400) {
    throw new Error(`sentinel_req_failed_${Number(matchedReqStatus)}`);
  }
  const turnstileRequired = !!(reqPayload.turnstile && reqPayload.turnstile.required);
  const soRequired = !!(reqPayload.so && reqPayload.so.required);
  const proofRequired = !!(reqPayload.proofofwork && reqPayload.proofofwork.required);
  if (turnstileRequired && !toStringValue(tokenMeta && tokenMeta.sentinelPayload && tokenMeta.sentinelPayload.t).trim()) {
    throw new Error("sentinel_turnstile_token_failed");
  }
  if (!!input.includeSo && soRequired) {
    const soError = toStringValue(browserResult && browserResult.soError).trim();
    if (soError) {
      throw new Error(`sentinel_so_token_failed:${soError}`);
    }
    if (!toStringValue(browserResult && browserResult.soHeader).trim()) {
      throw new Error("sentinel_so_token_failed");
    }
    if (!toStringValue(tokenMeta && tokenMeta.soPayload && tokenMeta.soPayload.so).trim()) {
      throw new Error("sentinel_so_token_failed");
    }
  }
  return { turnstileRequired, soRequired, proofRequired };
}

async function attachSentinelReqObserver(context, page) {
  const cdp = await context.newCDPSession(page);
  await cdp.send("Network.enable");
  const trackedRequests = new Map();
  const pendingBodies = new Set();
  const state = {
    count: 0,
    status: 0,
    requests: [],
    payload: {},
    matchedCount: 0,
  };
  cdp.on("Network.responseReceived", (event) => {
    const response = event && typeof event === "object" ? event.response : null;
    if (!response || !isSentinelReqUrl(response.url)) {
      return;
    }
    const requestId = toStringValue(event.requestId).trim();
    if (!requestId) {
      return;
    }
    state.count += 1;
    const status = Number(response.status) || 0;
    if (status > 0) {
      state.status = status;
    }
    trackedRequests.set(requestId, { status });
  });
  cdp.on("Network.loadingFinished", (event) => {
    const requestId = toStringValue(event && event.requestId).trim();
    const tracked = requestId ? trackedRequests.get(requestId) : null;
    if (!requestId || !tracked) {
      return;
    }
    const pending = cdp
      .send("Network.getResponseBody", { requestId })
      .then((bodyResult) => {
        const payload = parseResponseBody(bodyResult);
        state.requests.push({
          status: Number(tracked.status) || 0,
          payload: payload && typeof payload === "object" ? payload : {},
        });
      })
      .catch(() => {})
      .finally(() => {
        trackedRequests.delete(requestId);
        pendingBodies.delete(pending);
      });
    pendingBodies.add(pending);
  });
  cdp.on("Network.loadingFailed", (event) => {
    const requestId = toStringValue(event && event.requestId).trim();
    if (requestId) {
      trackedRequests.delete(requestId);
    }
  });
  state.waitForCapture = async (timeoutMs) => {
    const deadline = Date.now() + Math.max(0, Number(timeoutMs) || 0);
    while (true) {
      if (state.count > 0 && pendingBodies.size === 0) {
        return;
      }
      if (Date.now() >= deadline) {
        if (pendingBodies.size > 0) {
          await Promise.allSettled(Array.from(pendingBodies));
        }
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  };
  return state;
}

async function launchBrowser(chromium, input) {
  const proxy = parseProxy(input.proxy);
  const timeout = Math.max(1000, Number(input.launchTimeoutMs) || 60000);
  const args = [
    "--disable-blink-features=AutomationControlled",
    "--no-first-run",
    "--no-default-browser-check",
  ];
  const candidates = [];
  const browserPath = toStringValue(input.browserPath).trim();
  const browserChannel = toStringValue(input.browserChannel).trim();
  if (browserPath) {
    candidates.push({ executablePath: browserPath, headless: true, proxy, args, timeout });
  }
  if (browserChannel) {
    candidates.push({ channel: browserChannel, headless: true, proxy, args, timeout });
  }
  candidates.push({ channel: "chrome", headless: true, proxy, args, timeout });
  candidates.push({ channel: "msedge", headless: true, proxy, args, timeout });
  candidates.push({ headless: true, proxy, args, timeout });
  const errors = [];
  for (const candidate of candidates) {
    try {
      if (!candidate.proxy) {
        delete candidate.proxy;
      }
      return await chromium.launch(candidate);
    } catch (error) {
      const label = candidate.executablePath || candidate.channel || "bundled";
      const message = error && error.message ? error.message : String(error || "unknown");
      errors.push(`${label}:${message}`);
    }
  }
  throw new Error(`sentinel_browser_launch_failed:${errors.join(" | ")}`);
}

async function applyClientHints(context, page, input) {
  const headers = {};
  const secChUa = toStringValue(input.secChUa).trim();
  const secChUaFullVersionList = toStringValue(input.secChUaFullVersionList).trim();
  if (secChUa) {
    headers["sec-ch-ua"] = secChUa;
  }
  if (secChUaFullVersionList) {
    headers["sec-ch-ua-full-version-list"] = secChUaFullVersionList;
  }
  headers["sec-ch-ua-mobile"] = "?0";
  headers["sec-ch-ua-platform"] = '"Windows"';
  await context.setExtraHTTPHeaders(headers);
  const userAgent = toStringValue(input.userAgent).trim();
  if (!userAgent) {
    return;
  }
  const payload = {
    userAgent,
    acceptLanguage: "en-US,en",
    platform: "Windows",
    userAgentMetadata: buildUserAgentMetadata(input),
  };
  const cdp = await context.newCDPSession(page);
  try {
    await cdp.send("Emulation.setUserAgentOverride", payload);
  } catch {
    try {
      await cdp.send("Network.setUserAgentOverride", payload);
    } catch {
    }
  }
}

async function gotoPageWithRetry(page, input) {
  const pageUrl = toStringValue(input.pageUrl).trim() || "https://chatgpt.com/";
  let lastGotoError = null;
  for (let attempt = 0; attempt < 2; attempt += 1) {
    try {
      await page.goto(pageUrl, {
        waitUntil: "domcontentloaded",
        timeout: Math.max(1000, Number(input.navigationTimeoutMs) || 60000),
      });
      lastGotoError = null;
      break;
    } catch (error) {
      lastGotoError = error;
      if (attempt < 1) {
        await page.waitForTimeout(1000);
      }
    }
  }
  if (lastGotoError) {
    const message = lastGotoError && lastGotoError.message ? lastGotoError.message : String(lastGotoError || "unknown");
    throw new Error(`sentinel_browser_page_load_failed:${message}`);
  }
}

async function loadSentinelSdk(page, input) {
  return page.evaluate(
    async ({ wrapperUrl }) => {
      await new Promise((resolve, reject) => {
        const script = document.createElement("script");
        script.src = wrapperUrl;
        script.onload = resolve;
        script.onerror = () => reject(new Error("sentinel_sdk_load_failed"));
        document.head.appendChild(script);
      });
      if (!window.SentinelSDK) {
        throw new Error("sentinel_sdk_missing");
      }
      let sdkUrl = "";
      let sdkVersion = "";
      for (const script of Array.from(document.scripts || [])) {
        const src = script && typeof script.src === "string" ? script.src : "";
        const match = /\/sentinel\/([^/]+)\/sdk\.js/.exec(src);
        if (match) {
          sdkUrl = src;
          sdkVersion = match[1] || "";
          break;
        }
      }
      return { sdkUrl, sdkVersion };
    },
    {
      wrapperUrl: toStringValue(input.wrapperUrl).trim() || "https://chatgpt.com/backend-api/sentinel/sdk.js",
    },
  );
}

async function main() {
  const rawInput = await readStdin();
  const input = rawInput.trim() ? JSON.parse(rawInput) : {};
  const playwright = loadPlaywright();
  const browser = await launchBrowser(playwright.chromium, input);
  try {
    const context = await browser.newContext({
      userAgent: toStringValue(input.userAgent).trim() || undefined,
      viewport: { width: 1365, height: 768 },
    });
    const pageUrl = toStringValue(input.pageUrl).trim() || "https://chatgpt.com/";
    const cookies = normalizeCookies(input.cookies, pageUrl);
    if (cookies.length > 0) {
      await context.addCookies(cookies);
    }
    const page = await context.newPage();
    await applyClientHints(context, page, input);
    await gotoPageWithRetry(page, input);
    const runtimeSdk = await loadSentinelSdk(page, input);
    const reqObserver = await attachSentinelReqObserver(context, page);
    const browserResult = await page.evaluate(
      async ({ flow, waitMs, includeSo, sdkUrl, sdkVersion }) => {
        if (typeof window.SentinelSDK.init === "function") {
          await window.SentinelSDK.init(flow);
        }
        if (typeof window.SentinelSDK.token !== "function") {
          throw new Error("sentinel_sdk_token_missing");
        }
        const sentinelHeader = String((await window.SentinelSDK.token(flow)) || "");
        let soHeader = "";
        let soError = "";
        if (includeSo && typeof window.SentinelSDK.sessionObserverToken === "function") {
          if (Math.max(0, Number(waitMs) || 0) > 0) {
            await new Promise((resolve) => setTimeout(resolve, Math.max(0, Number(waitMs) || 0)));
          }
          try {
            soHeader = String((await window.SentinelSDK.sessionObserverToken(flow)) || "");
          } catch (error) {
            soError = error && error.message ? error.message : String(error || "sentinel_so_failed");
          }
        }
        return { sentinelHeader, soHeader, soError, sdkUrl, sdkVersion };
      },
      {
        flow: toStringValue(input.flow).trim() || "chat-requirements",
        waitMs: Math.max(0, Number(input.observerWaitMs) || 0),
        includeSo: !!input.includeSo,
        sdkUrl: runtimeSdk.sdkUrl,
        sdkVersion: runtimeSdk.sdkVersion,
      },
    );
    await reqObserver.waitForCapture(Math.max(3000, Number(input.reqCaptureWaitMs) || 10000));
    const tokenMeta = challengeTokenFromHeaders(browserResult.sentinelHeader, browserResult.soHeader);
    let matchedReqPayload = {};
    let matchedReqStatus = reqObserver.status;
    let matchedReqCount = 0;
    if (tokenMeta.challengeToken) {
      const matches = reqObserver.requests.filter((item) => {
        const payload = item && typeof item === "object" ? item.payload : null;
        return toStringValue(payload && payload.token).trim() === tokenMeta.challengeToken;
      });
      matchedReqCount = matches.length;
      if (matches.length > 0) {
        const latest = matches[matches.length - 1];
        matchedReqPayload = latest && typeof latest === "object" ? latest.payload || {} : {};
        matchedReqStatus = Number(latest && latest.status) || matchedReqStatus;
      }
    }
    if (!!input.runtimeCheckOnly) {
      const runtimeMeta = validateRuntimeCheck(input, browserResult, tokenMeta, matchedReqPayload, matchedReqCount, matchedReqStatus);
      process.stdout.write(JSON.stringify({
        ok: true,
        runtimeReady: true,
        sdkUrl: runtimeSdk.sdkUrl,
        sdkVersion: runtimeSdk.sdkVersion,
        sentinelTokenLen: toStringValue(browserResult.sentinelHeader).length,
        soTokenLen: toStringValue(browserResult.soHeader).length,
        reqStatus: matchedReqStatus,
        reqMatchedCount: matchedReqCount,
        proofRequired: runtimeMeta.proofRequired,
        turnstileRequired: runtimeMeta.turnstileRequired,
        soRequired: runtimeMeta.soRequired,
        executor: "browser_sdk",
      }));
      await browser.close();
      return;
    }
    const result = {
      ok: true,
      sentinelHeader: browserResult.sentinelHeader,
      soHeader: browserResult.soHeader,
      soError: browserResult.soError,
      sdkUrl: browserResult.sdkUrl,
      sdkVersion: browserResult.sdkVersion,
      reqPayload: matchedReqPayload,
      reqCount: reqObserver.count,
      reqMatchedCount: matchedReqCount,
      reqStatus: matchedReqStatus,
      challengeMismatch: tokenMeta.challengeMismatch,
      sentinelPayload: tokenMeta.sentinelPayload,
      soPayload: tokenMeta.soPayload,
      executor: "browser_sdk",
    };
    process.stdout.write(JSON.stringify(result));
    await browser.close();
  } catch (error) {
    try {
      await browser.close();
    } catch {
    }
    throw error;
  }
}

main().catch((error) => {
  const message = error && error.message ? error.message : String(error || "unknown");
  process.stderr.write(JSON.stringify({ ok: false, error: message }));
  process.exit(1);
});
