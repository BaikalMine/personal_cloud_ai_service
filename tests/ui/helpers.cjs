const { expect } = require("@playwright/test");

const settlePage = async (page) => {
  await page.waitForLoadState("domcontentloaded");
  await page.evaluate(async () => {
    if (document.fonts?.ready) await document.fonts.ready;
  });
  await page.addStyleTag({ content: "*, *::before, *::after { animation-duration: 0s !important; transition-duration: 0s !important; scroll-behavior: auto !important; }" });
};

const assertNoViewportOverflow = async (page, label) => {
  const report = await page.evaluate(() => {
    const root = document.documentElement;
    const textSelectors = "main button, main a, main label, main summary, main th, main td, main strong, main p, main small, main span, main output";
    const textOverflow = [...document.querySelectorAll(textSelectors)].flatMap((element) => {
      const rect = element.getBoundingClientRect();
      if (!element.textContent?.trim() || element.children.length || rect.width < 1 || rect.height < 1) return [];
      const style = getComputedStyle(element);
      if (style.display === "inline" || ["hidden", "clip", "auto", "scroll"].includes(style.overflowX) || style.textOverflow === "ellipsis") return [];
      if (element.scrollWidth <= element.clientWidth + 1) return [];
      return [{
        tag: element.tagName.toLowerCase(),
        className: String(element.className || "").slice(0, 120),
        text: element.textContent.trim().replace(/\s+/g, " ").slice(0, 120),
        width: Math.round(element.clientWidth),
        scrollWidth: Math.round(element.scrollWidth),
      }];
    });
    return {
      documentOverflow: Math.max(0, root.scrollWidth - root.clientWidth),
      viewport: root.clientWidth,
      scrollWidth: root.scrollWidth,
      textOverflow,
    };
  });

  expect.soft(report.documentOverflow, `${label}: viewport ${report.viewport}px, document ${report.scrollWidth}px`).toBeLessThanOrEqual(1);
  expect.soft(report.textOverflow, `${label}: text exceeds its own container`).toEqual([]);
};

const expectFocusInside = async (page, selector) => {
  const inside = await page.evaluate((rootSelector) => {
    const root = document.querySelector(rootSelector);
    return Boolean(root && document.activeElement && root.contains(document.activeElement));
  }, selector);
  expect(inside).toBe(true);
};

module.exports = { settlePage, assertNoViewportOverflow, expectFocusInside };
