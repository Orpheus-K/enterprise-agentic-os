import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./app";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  document.documentElement.classList.remove("dark");
});

it("reports a real connection failure instead of showing a ready state", async () => {
  vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
  render(<App />);
  fireEvent.click(screen.getByRole("button", { name: "检查连接" }));
  expect(await screen.findByText(/连接检查失败/)).toBeDefined();
  expect(screen.queryByText(/核心进程可达/)).toBeNull();
});

it("switches the shared theme with an accessible toggle", () => {
  render(<App />);
  const button = screen.getByRole("button", { name: "深色模式" });
  fireEvent.click(button);
  expect(button.getAttribute("aria-pressed")).toBe("true");
  expect(document.documentElement.classList.contains("dark")).toBe(true);
});
