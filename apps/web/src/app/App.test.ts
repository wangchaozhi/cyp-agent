import { describe, expect, it } from "vitest";

import { positionPollingInterval } from "./App";

describe("positionPollingInterval", () => {
  it("uses low-frequency synchronization when there are no positions", () => {
    expect(positionPollingInterval(0)).toBe(60_000);
  });

  it("restores active monitoring when a position exists", () => {
    expect(positionPollingInterval(1)).toBe(5_000);
  });
});
