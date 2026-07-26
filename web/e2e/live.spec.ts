import { test, expect } from "@playwright/test";

/**
 * The push stream against the real binary.
 *
 * Counting handshakes is the only way to catch this: a socket that is torn down
 * and immediately redialled looks fine in a screenshot a second later, and the
 * only symptom anyone reports is the connection light flickering through
 * "Reconnecting…" on every click.
 */
test("navigating between pages keeps one socket open", async ({ page }) => {
  const opened: string[] = [];
  page.on("websocket", (ws) => opened.push(ws.url()));

  await page.goto("/");
  await expect(page.getByText("Test Comic")).toBeVisible();
  await expect.poll(() => opened.length).toBe(1);

  for (const label of ["Collections", "Tags", "Import", "Settings", "Library"]) {
    await page.getByRole("link", { name: label, exact: true }).click();
    await expect(page.locator(".conn-dot--open").first()).toBeVisible();
  }

  expect(opened).toHaveLength(1);
});
