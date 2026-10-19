import { expect, test } from "@playwright/test";

test("serves the console and real core contract without a Node server", async ({
  page,
}) => {
  await page.goto("/skills/example");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "企业级数字员工运行底座",
  );
  await page.getByRole("button", { name: "检查连接" }).click();
  await expect(page.getByRole("status")).toHaveText(
    "核心进程可达；不代表业务就绪",
  );
  await page.screenshot({
    path: "test-results/console-light.png",
    fullPage: true,
    animations: "disabled",
  });
  const action = page.getByRole("button", { name: "检查连接" });
  const lightBackground = await action.evaluate(
    (element) => getComputedStyle(element).backgroundColor,
  );
  await page.getByRole("button", { name: "深色模式" }).click();
  await expect(page.locator("html")).toHaveClass("dark");
  await expect
    .poll(() =>
      action.evaluate((element) => getComputedStyle(element).backgroundColor),
    )
    .not.toBe(lightBackground);
  await page.screenshot({
    path: "test-results/console-dark.png",
    fullPage: true,
    animations: "disabled",
  });
});

test("supports mobile width and keyboard activation", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "深色模式" })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("button", { name: "深色模式" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
});
