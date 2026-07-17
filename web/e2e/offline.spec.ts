import { test, expect, type Page, type BrowserContext } from "@playwright/test";

/**
 * The offline path, driven against the real binary.
 *
 * Every test here starts online and downloads the comic first, because that is
 * the only way to reach the state the suite is about: an app with something on
 * disk worth showing when the server goes away. A test that goes offline with
 * an empty cache proves nothing — everything is correctly broken.
 */

const COMIC = "Test Comic";

/**
 * The service worker is what answers offline, and it answers nothing until it
 * controls the page. Reloading before then races activation and fails in a way
 * that looks exactly like the bug under test, so every test waits here first.
 */
async function waitForController(page: Page) {
  await page.waitForFunction(() => Boolean(navigator.serviceWorker.controller), null, {
    timeout: 20_000,
  });
}

/**
 * Downloads the comic and comes back to the shelf.
 *
 * The download button lives in the reader's toolbar and nowhere else, so this
 * has to open the comic to reach it — there is no download affordance on a
 * library card.
 */
async function downloadComic(page: Page) {
  await page.getByText(COMIC).click();
  await expect(page).toHaveURL(/\/comic\//);
  await page.getByRole("button", { name: "Download for offline reading" }).click();
  await expect(page.getByRole("img", { name: "Saved for offline reading" })).toBeVisible({
    timeout: 30_000,
  });
  await page.goto("/");
  await expect(page.getByText(COMIC)).toBeVisible();
}

/** Fails every API call the page makes, as a reachable-but-broken server would. */
async function breakServer(context: BrowserContext) {
  await context.route("**/api/**", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "db error" }),
    }),
  );
  await context.route("**/auth/**", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "db error" }),
    }),
  );
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText(COMIC)).toBeVisible();
  await waitForController(page);
});

test("a cold reload with no network keeps the session", async ({ page, context }) => {
  await downloadComic(page);

  await context.setOffline(true);
  await page.reload();

  // The bug: fetch() rejects, the session query errors, and a null user is
  // indistinguishable from a signed-out one, so the app redirects to a login
  // page that cannot reach the server either.
  await expect(page).not.toHaveURL(/\/login/);
  await expect(page.getByText(COMIC)).toBeVisible();
});

test("a downloaded comic is readable with no network", async ({ page, context }) => {
  await downloadComic(page);

  await context.setOffline(true);
  await page.reload();
  await page.getByText(COMIC).click();

  await expect(page).toHaveURL(/\/comic\//);
  const firstPage = page.getByRole("img", { name: "Page 1" }).first();
  await expect(firstPage).toBeVisible();
  // Visible only means the <img> laid out; a broken image is visible too. The
  // bytes have to have come back from the page cache.
  await expect
    .poll(() => firstPage.evaluate((img: HTMLImageElement) => img.naturalWidth), {
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
});

test("a broken server says so once and stays usable", async ({ page, context }) => {
  await downloadComic(page);
  await breakServer(context);
  await page.reload();

  await expect(page.getByText("Dowitcher's server is having trouble")).toBeVisible();
  // The point of saying it at all: the shelf is still there, served from disk.
  await expect(page.getByText(COMIC)).toBeVisible();
  await expect(page).not.toHaveURL(/\/login/);
});

test("the outage notice does not repeat once it has been said", async ({ page, context }) => {
  await downloadComic(page);
  await breakServer(context);
  await page.reload();

  const notice = page.getByText("Dowitcher's server is having trouble");
  await expect(notice).toBeVisible();

  // A shelf full of failing queries is many 5xx, not one. Navigating produces
  // more. One notice, however many times the server says no.
  //
  // Scoped to the sidebar because the mobile tab bar carries the same labels.
  const nav = page.getByRole("navigation", { name: "Main" });
  await nav.getByRole("link", { name: "Offline" }).click();
  await nav.getByRole("link", { name: "Library" }).click();
  await expect(notice).toHaveCount(1);
});

test("a real 401 still goes to the login page", async ({ page, context }) => {
  // The fix teaches the app to keep going when the session check fails. This is
  // the line it must not cross: a server that answers "not authenticated" has
  // answered, and offline tolerance must not turn into never signing out.
  await context.route("**/auth/me", (route) =>
    route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ error: "not authenticated" }),
    }),
  );
  await page.reload();

  await expect(page).toHaveURL(/\/login/);
});
