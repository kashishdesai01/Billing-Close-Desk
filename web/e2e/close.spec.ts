import { test, expect } from "@playwright/test";
test("frozen preview, independent approval, issue, credit and ERP delivery", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/");
  await page.getByRole("button", { name: "Maya Chen Analyst" }).click();
  await expect(
    page.getByRole("heading", { name: "Every detail, accounted for." }),
  ).toBeVisible();
  await page.getByLabel("Billing period").selectOption("2026-08-01");
  await expect(
    page.getByRole("button", { name: "Preview", exact: true }),
  ).toHaveCount(4);
  await page
    .locator(".customer-grid article")
    .filter({ has: page.getByRole("heading", { name: "Northstar Media" }) })
    .getByRole("button", { name: "Preview" })
    .click();
  await expect(page.getByRole("status")).toContainText("Change recorded");
  await page
    .getByRole("button", { name: "Open Northstar Media invoice" })
    .click();
  await page.getByRole("button", { name: "Submit for approval" }).click();
  await page.getByRole("button", { name: "Approvals", exact: true }).click();
  await expect(page.getByText("Another person must review")).toBeVisible();
  await page.getByLabel("Demo persona").selectOption("alex");
  await page.getByRole("button", { name: "Approve", exact: true }).click();
  await page.getByLabel("Demo persona").selectOption("sam");
  await page.getByRole("button", { name: "Invoices", exact: true }).click();
  await page
    .getByRole("button", { name: "Open Northstar Media invoice" })
    .click();
  await page
    .getByRole("button", { name: "Issue invoice", exact: true })
    .click();
  await expect(page.locator("table")).toContainText(/INV-\d+/);
  await page
    .getByRole("button", { name: "Open Northstar Media invoice" })
    .click();
  await page.getByRole("button", { name: "Request credit" }).click();
  await page.getByLabel("Credit amount (USD)").fill("25.00");
  await page
    .getByLabel("Reason", { exact: true })
    .fill("Synthetic goodwill adjustment");
  await page.getByRole("button", { name: "Create credit request" }).click();
  await page.getByLabel("Demo persona").selectOption("alex");
  await page.getByRole("button", { name: "Approvals", exact: true }).click();
  await page.getByRole("button", { name: "Approve", exact: true }).click();
  await page.getByLabel("Demo persona").selectOption("sam");
  await page.getByRole("button", { name: "Issue credit", exact: true }).click();
  await page.getByRole("button", { name: "Integrations", exact: true }).click();
  await page.getByRole("button", { name: "Run delivery tick" }).click();
  await expect(page.getByRole("status")).toContainText("tick finished");
  await expect(page.locator("tbody .badge.confirmed")).toHaveCount(2);
  await page.getByRole("button", { name: "Journal", exact: true }).click();
  await expect(page.locator("table")).toContainText("Credit issued");
  expect(errors).toEqual([]);
});
test("viewer cannot create financial records and layout supports small screens", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await page.getByRole("button", { name: "Guest observer Viewer" }).click();
  await expect(
    page.getByRole("heading", { name: "Every detail, accounted for." }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Preview", exact: true }),
  ).toHaveCount(0);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
});

test("approvers see frozen evidence and creators can withdraw pending work", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Maya Chen Analyst" }).click();
  await page.getByLabel("Billing period").selectOption("2026-08-01");
  await page
    .locator(".customer-grid article")
    .filter({ has: page.getByRole("heading", { name: "Cedar & Coast" }) })
    .getByRole("button", { name: "Preview" })
    .click();
  await page
    .getByRole("button", { name: "Open Cedar & Coast invoice" })
    .click();
  await page.getByRole("button", { name: "Submit for approval" }).click();
  await page.getByLabel("Demo persona").selectOption("alex");
  await page.getByRole("button", { name: "Approvals", exact: true }).click();
  const evidence = page.getByLabel("Approval evidence");
  await expect(evidence).toContainText("825,000 impressions");
  await expect(evidence).toContainText("$8.500000 CPM");
  await expect(evidence).toContainText("Revision 1");
  await expect(evidence).toContainText("cedar-v1");
  await page.getByLabel("Demo persona").selectOption("maya");
  await page.getByRole("button", { name: "Invoices", exact: true }).click();
  await page
    .getByRole("button", { name: "Open Cedar & Coast invoice" })
    .click();
  page.once("dialog", (dialog) =>
    dialog.accept("Withdraw pending invoice for correction"),
  );
  await page.getByRole("button", { name: "Discard draft" }).click();
  await expect(page.getByRole("status")).toContainText("Change recorded");
  await page.getByRole("button", { name: "Approvals", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Approve", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Invoices", exact: true }).click();
  await expect(page.locator("tbody .badge.cancelled")).toBeVisible();
  await expect(
    page
      .locator(".customer-grid article")
      .filter({ has: page.getByRole("heading", { name: "Cedar & Coast" }) })
      .getByRole("button", { name: "Preview" }),
  ).toBeEnabled();
});

test("failed logout preserves the session and expired sessions return to sign-in", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Sam Okafor Controller" }).click();
  await expect(
    page.getByRole("heading", { name: "Every detail, accounted for." }),
  ).toBeVisible();
  await page.route("**/auth/logout", (route) =>
    route.fulfill({ status: 503, body: "unavailable" }),
  );
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("alert")).toContainText("Sign-out failed");
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
  await page.unroute("**/auth/logout");
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(
    page.getByRole("heading", { name: "Good books. Clear decisions." }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Sam Okafor Controller" }).click();
  await expect(
    page.getByRole("heading", { name: "Every detail, accounted for." }),
  ).toBeVisible();
  await page.route("**/api/state?*", (route) =>
    route.fulfill({ status: 401, body: "expired" }),
  );
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Good books. Clear decisions." }),
  ).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("session expired");
  await expect(
    page.getByRole("button", { name: "Close period", exact: true }),
  ).toHaveCount(0);
});

test("a complete browser-driven month reconciles and closes", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Maya Chen Analyst" }).click();
  for (const customer of [
    "Atlas Audio",
    "Cedar & Coast",
    "Kinetic Studio",
    "Northstar Media",
  ]) {
    await page.getByLabel("Demo persona").selectOption("maya");
    await page.getByRole("button", { name: "Invoices", exact: true }).click();
    await page
      .locator(".customer-grid article")
      .filter({
        has: page.getByRole("heading", { name: customer, exact: true }),
      })
      .getByRole("button", { name: "Preview" })
      .click();
    await page
      .getByRole("button", { name: `Open ${customer} invoice` })
      .click();
    await page.getByRole("button", { name: "Submit for approval" }).click();
    await page.getByLabel("Demo persona").selectOption("alex");
    await page.getByRole("button", { name: "Approvals", exact: true }).click();
    await expect(page.getByLabel("Approval evidence")).toContainText(
      "Revision 1",
    );
    await page.getByRole("button", { name: "Approve", exact: true }).click();
    await page.getByLabel("Demo persona").selectOption("sam");
    await page.getByRole("button", { name: "Invoices", exact: true }).click();
    await page
      .getByRole("button", { name: `Open ${customer} invoice` })
      .click();
    await page
      .getByRole("button", { name: "Issue invoice", exact: true })
      .click();
  }
  await page.getByRole("button", { name: "Integrations", exact: true }).click();
  await page.getByRole("button", { name: "Run delivery tick" }).click();
  await expect(page.locator("tbody .badge.confirmed")).toHaveCount(4);
  await page
    .getByRole("button", { name: "Reconciliation", exact: true })
    .click();
  await page.getByRole("button", { name: "Run reconciliation" }).click();
  await expect(page.getByRole("status")).toContainText("0 open discrepancies");
  await page.getByRole("button", { name: "Overview", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Close period", exact: true }),
  ).toBeEnabled();
  page.once("dialog", (dialog) =>
    dialog.accept("All records reviewed and reconciled in browser"),
  );
  await page.getByRole("button", { name: "Close period", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Reopen period", exact: true }),
  ).toBeVisible();
  await expect(page.locator(".close-panel .badge")).toHaveText("closed");
  await expect(
    page.getByRole("button", { name: "Preview", exact: true }),
  ).toHaveCount(0);
});
