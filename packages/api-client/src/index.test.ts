import { afterEach, describe, expect, it, vi } from "vitest";
import { getCoreLiveness } from "./index";

afterEach(() => vi.unstubAllGlobals());

describe("core liveness boundary", () => {
  it("accepts the documented response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(Response.json({ status: "ok" })),
    );
    await expect(getCoreLiveness()).resolves.toEqual({ status: "ok" });
  });

  it.each([{ status: "ready" }, {}, { status: "ok", secret: "unexpected" }])(
    "rejects an invalid response: %j",
    async (payload) => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(Response.json(payload)));
      await expect(getCoreLiveness()).rejects.toThrow("violates the contract");
    },
  );

  it("does not mask HTTP failures", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(null, { status: 503 })),
    );
    await expect(getCoreLiveness()).rejects.toThrow("HTTP 503");
  });
});
